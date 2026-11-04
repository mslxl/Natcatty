/*
 * Reference display harness built from the stock mosh 1.4.0 terminal sources.
 * It reads a length-prefixed event stream, feeds it through stock's UTF8Parser
 * and Emulator, and writes the exact Display::new_frame bytes for each step.
 *
 * Event stream (little-endian):
 *   u32 width, u32 height, u32 event_count
 *   event: u8 type
 *     type 0: u32 len, bytes[ len ]        (terminal input)
 *     type 1: u32 width, u32 height        (remote resize)
 *
 * Output stream (little-endian):
 *   u32 frame_count
 *   frame: u32 len, bytes[ len ]
 *
 * The Display capability constructor is defined here so the harness can link
 * without ncurses; it matches Display(false), the MOSH_NO_TERM_INIT defaults.
 */

#include <limits.h>
#include <locale.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <vector>
#include <wchar.h>

#include "parser.h"
#include "terminal.h"
#include "terminaldisplay.h"
#include "terminalframebuffer.h"

namespace Terminal {
  Display::Display( bool use_environment )
    : has_ech( true ), has_bce( true ), has_title( true ), smcup( NULL ), rmcup( NULL )
  {
    (void)use_environment;
  }
}

using namespace Parser;
using namespace Terminal;

static std::string hex_bytes( const std::string &s )
{
  static const char digits[] = "0123456789abcdef";
  std::string out;
  out.reserve( s.size() * 2 );
  for ( size_t i = 0; i < s.size(); i++ ) {
    unsigned char c = (unsigned char)s[i];
    out.push_back( digits[c >> 4] );
    out.push_back( digits[c & 0xf] );
  }
  return out;
}

static std::string wide_to_utf8( const std::vector<wchar_t> &input )
{
  std::string out;
  mbstate_t state;
  memset( &state, 0, sizeof( state ) );
  char buf[MB_LEN_MAX];
  for ( size_t i = 0; i < input.size(); i++ ) {
    size_t n = wcrtomb( buf, input[i], &state );
    if ( n == (size_t)-1 ) {
      memset( &state, 0, sizeof( state ) );
      continue;
    }
    out.append( buf, n );
  }
  return out;
}

/* A deterministic dump of the terminal state. The Go golden test uses the
   same field order so a stock screen can be committed and compared without
   re-running Docker. Cell contents use Cell::print_grapheme, plus explicit
   empty/wide/fallback flags and the rendition SGR. */
static std::string screen_dump( const Framebuffer &fb )
{
  const DrawState &ds = fb.ds;
  std::string out;
  char line[256];
  snprintf( line, sizeof( line ), "size\t%d\t%d\n", ds.get_width(), ds.get_height() );
  out += line;
  snprintf( line, sizeof( line ), "cursor\t%d\t%d\n", ds.get_cursor_col(), ds.get_cursor_row() );
  out += line;
  snprintf( line, sizeof( line ),
            "modes\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
            ds.cursor_visible ? 1 : 0, ds.reverse_video ? 1 : 0,
            ds.bracketed_paste ? 1 : 0, (int)ds.mouse_reporting_mode,
            ds.mouse_focus_event ? 1 : 0, ds.mouse_alternate_scroll ? 1 : 0,
            (int)ds.mouse_encoding_mode, ds.application_mode_cursor_keys ? 1 : 0,
            ds.auto_wrap_mode ? 1 : 0, ds.origin_mode ? 1 : 0,
            ds.insert_mode ? 1 : 0, ds.next_print_will_wrap ? 1 : 0,
            ds.get_background_rendition() );
  out += line;
  snprintf( line, sizeof( line ), "scroll\t%d\t%d\n",
            ds.get_scrolling_region_top_row(), ds.get_scrolling_region_bottom_row() );
  out += line;
  snprintf( line, sizeof( line ), "bell\t%u\n", fb.get_bell_count() );
  out += line;
  snprintf( line, sizeof( line ), "title_init\t%d\n", fb.is_title_initialized() ? 1 : 0 );
  out += line;
  out += "title\t" + hex_bytes( wide_to_utf8( fb.get_window_title() ) ) + "\n";
  out += "icon\t" + hex_bytes( wide_to_utf8( fb.get_icon_name() ) ) + "\n";
  out += "clipboard\t" + hex_bytes( wide_to_utf8( fb.get_clipboard() ) ) + "\n";

  const Framebuffer::rows_type &rows = fb.get_rows();
  for ( size_t y = 0; y < rows.size(); y++ ) {
    const Row *row = fb.get_row( (int)y );
    snprintf( line, sizeof( line ), "row\t%zu\t%d", y, row->get_wrap() ? 1 : 0 );
    out += line;
    for ( size_t x = 0; x < row->cells.size(); x++ ) {
      const Cell &cell = row->cells[x];
      std::string content;
      cell.print_grapheme( content );
      snprintf( line, sizeof( line ), "\t%d,%d,%d,%s,%s",
                cell.empty() ? 1 : 0, cell.get_wide() ? 1 : 0,
                cell.get_fallback() ? 1 : 0,
                hex_bytes( cell.get_renditions().sgr() ).c_str(),
                hex_bytes( content ).c_str() );
      out += line;
    }
    out += "\n";
  }
  return out;
}

static uint32_t read_u32( FILE *f )
{
  unsigned char b[4];
  if ( fread( b, 1, 4, f ) != 4 ) {
    fprintf( stderr, "short read\n" );
    exit( 2 );
  }
  return (uint32_t)b[0] | (uint32_t)b[1] << 8 | (uint32_t)b[2] << 16 | (uint32_t)b[3] << 24;
}

static void write_u32( FILE *f, uint32_t v )
{
  unsigned char b[4] = { (unsigned char)(v & 0xff), (unsigned char)(v >> 8 & 0xff),
                         (unsigned char)(v >> 16 & 0xff), (unsigned char)(v >> 24 & 0xff) };
  if ( fwrite( b, 1, 4, f ) != 4 ) {
    fprintf( stderr, "short write\n" );
    exit( 2 );
  }
}

int main( int argc, char **argv )
{
  (void)argc;
  (void)argv;

  /* mosh-client refuses to run outside a UTF-8 locale; its wcwidth and
     wcrtomb behavior, which this oracle reproduces, depend on it. */
  if ( setlocale( LC_ALL, "C.UTF-8" ) == NULL ) {
    fprintf( stderr, "C.UTF-8 locale is unavailable\n" );
    return 2;
  }

  uint32_t width = read_u32( stdin );
  uint32_t height = read_u32( stdin );
  uint32_t events = read_u32( stdin );

  UTF8Parser parser;
  Emulator emulator( width, height );
  Display display( false );
  Actions actions;

  Framebuffer last = emulator.get_fb();
  std::string initial = display.new_frame( false, last, emulator.get_fb() );

  std::vector<Framebuffer> screens;
  screens.push_back( emulator.get_fb() );

  uint32_t frame_count = events + 1;
  write_u32( stdout, frame_count );
  write_u32( stdout, initial.size() );
  fwrite( initial.data(), 1, initial.size(), stdout );

  for ( uint32_t i = 0; i < events; i++ ) {
    int type = fgetc( stdin );
    if ( type == EOF ) {
      fprintf( stderr, "short event stream\n" );
      return 2;
    }
    if ( type == 0 ) {
      uint32_t len = read_u32( stdin );
      std::string input;
      input.resize( len );
      if ( len && fread( &input[0], 1, len, stdin ) != len ) {
        fprintf( stderr, "short input\n" );
        return 2;
      }
      for ( uint32_t j = 0; j < input.size(); j++ ) {
        parser.input( input[j], actions );
        for ( Actions::iterator it = actions.begin(); it != actions.end(); ++it ) {
          (*it)->act_on_terminal( &emulator );
        }
        actions.clear();
      }
      (void)emulator.read_octets_to_host();
    } else if ( type == 1 ) {
      uint32_t w = read_u32( stdin );
      uint32_t h = read_u32( stdin );
      Resize resize( w, h );
      resize.act_on_terminal( &emulator );
    } else {
      fprintf( stderr, "bad event type %d\n", type );
      return 2;
    }

    std::string frame = display.new_frame( true, last, emulator.get_fb() );
    write_u32( stdout, frame.size() );
    fwrite( frame.data(), 1, frame.size(), stdout );
    last = emulator.get_fb();
    screens.push_back( emulator.get_fb() );
  }

  /* After the frames, one deterministic screen dump per frame. */
  write_u32( stdout, (uint32_t)screens.size() );
  for ( size_t i = 0; i < screens.size(); i++ ) {
    std::string dump = screen_dump( screens[i] );
    write_u32( stdout, (uint32_t)dump.size() );
    fwrite( dump.data(), 1, dump.size(), stdout );
  }
  return 0;
}
