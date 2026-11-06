# mosh 1.3.2 predates C++17. The lab builds the pinned release with C++17
# because the runner's protobuf headers require it, and that standard removed
# std::ptr_fun, std::bind1st, std::bind2nd, and std::mem_fun_ref. Rewrite each
# call site as a lambda; the lambda performs the same call the erased adaptor
# performed, so the behavior is unchanged.
#
# Usage: awk -f patch-mosh-1.3.2.awk FILE > FILE.patched
#
# The replacement keeps the original leading whitespace, because the sources
# use tabs to align continuations.

# The using-declarations name identifiers C++17 no longer provides.
/^using std::(mem_fun_ref|bind2nd);$/ {
  next
}

# bind1st( ptr_fun( old_ack ), newest_echo_ack ) becomes old_ack( ack, p ).
/input_history\.remove_if\( bind1st\(/ {
  indent = substr($0, 1, index($0, "input_history") - 1)
  print indent "input_history.remove_if( [&]( const pair<uint64_t, uint64_t> &p ) { return old_ack( newest_echo_ack, p ); } );"
  next
}

# bind2nd( mem_fun_ref( &Member::fn ), arg ) becomes x.fn( arg ).
/cursors\.remove_if\( bind2nd\( mem_fun_ref\( &ConditionalCursorMove::tentative \)/ {
  indent = substr($0, 1, index($0, "cursors") - 1)
  print indent "cursors.remove_if( [&]( const ConditionalCursorMove &cursor ) { return cursor.tentative( epoch - 1 ); } );"
  next
}

/bind2nd\( mem_fun_ref\( &ConditionalOverlayRow::row_num_eq \)/ {
  indent = substr($0, 1, index($0, "bind2nd") - 1)
  print indent "[&]( const ConditionalOverlayRow &row ) { return row.row_num_eq( row_num ); } );"
  next
}

/bind2nd\( mem_fun_ref\( &TimestampedState<MyState>::num_eq \)/ {
  indent = substr($0, 1, index($0, "bind2nd") - 1)
  print indent "[&]( const TimestampedState<MyState> &state ) { return state.num_eq( ack_num ); } ) ) {"
  next
}

/sent_states\.remove_if\( bind2nd\( mem_fun_ref\( &TimestampedState<MyState>::num_lt \)/ {
  indent = substr($0, 1, index($0, "sent_states") - 1)
  print indent "sent_states.remove_if( [&]( const TimestampedState<MyState> &state ) { return state.num_lt( ack_num ); } );"
  next
}

{ print }
