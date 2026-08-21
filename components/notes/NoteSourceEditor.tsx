import React, { useEffect, useImperativeHandle, useRef } from "react";
import { type MarkdownActionType, wrapMarkdownSyntax } from "../../domain/notes";

export interface NoteSourceEditorHandle {
  insertAction: (action: MarkdownActionType) => void;
  focus: () => void;
}

export interface NoteSourceEditorProps {
  value: string;
  placeholder?: string;
  onChange: (value: string) => void;
  className?: string;
  noteFontFamily?: string;
  noteFontSize?: number;
}

export const NoteSourceEditor = React.forwardRef<NoteSourceEditorHandle, NoteSourceEditorProps>(
  ({ value, placeholder = "", onChange, className = "", noteFontFamily, noteFontSize }, ref) => {
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const lineNumbersRef = useRef<HTMLDivElement>(null);
    // Undo/redo history for the source textarea (native textarea undo is
    // unreliable once the value is controlled by React).
    const undoStackRef = useRef<string[]>([]);
    const redoStackRef = useRef<string[]>([]);
    // Tracks whether the latest value change originated from a user edit
    // (toolbar action / Tab) so external value swaps (e.g. switching notes)
    // can reset the history stacks instead of leaking into the next note.
    const userEditRef = useRef(false);

    // When the external value changes without a user edit (e.g. switching
    // notes), reset the undo/redo history so it never applies to another note.
    useEffect(() => {
      if (!userEditRef.current) {
        undoStackRef.current = [];
        redoStackRef.current = [];
      }
      userEditRef.current = false;
    }, [value]);

    const lineCount = (value.match(/\n/g)?.length || 0) + 1;
    const lineNumbers = Array.from({ length: lineCount }, (_, i) => i + 1);
    const gutterWidth = Math.max(48, String(lineCount).length * 9 + 24);

    const handleScroll = () => {
      if (textareaRef.current && lineNumbersRef.current) {
        lineNumbersRef.current.scrollTop = textareaRef.current.scrollTop;
      }
    };

    const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      const textarea = textareaRef.current;
      if (!textarea) return;

      // Handle Tab insertion
      if (e.key === "Tab") {
        e.preventDefault();
        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        const nextValue = `${value.substring(0, start)}  ${value.substring(end)}`;
        userEditRef.current = true;
        onChange(nextValue);
        requestAnimationFrame(() => {
          textarea.selectionStart = start + 2;
          textarea.selectionEnd = start + 2;
        });
      }
    };

    useImperativeHandle(ref, () => ({
      insertAction: (action: MarkdownActionType) => {
        const textarea = textareaRef.current;
        if (!textarea) return;

        if (action === "undo" || action === "redo") {
          const stack = action === "undo" ? undoStackRef.current : redoStackRef.current;
          const target = stack.pop();
          if (target === undefined) return;
          if (action === "undo") {
            redoStackRef.current.push(value);
          } else {
            undoStackRef.current.push(value);
          }
          userEditRef.current = true;
          onChange(target);
          requestAnimationFrame(() => {
            textarea.focus();
            textarea.setSelectionRange(target.length, target.length);
          });
          return;
        }

        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        const result = wrapMarkdownSyntax(value, start, end, action);
        if (result.text !== value) {
          undoStackRef.current.push(value);
          redoStackRef.current = [];
        }
        userEditRef.current = true;
        onChange(result.text);
        requestAnimationFrame(() => {
          textarea.focus();
          textarea.setSelectionRange(result.selectionStart, result.selectionEnd);
        });
      },
      focus: () => {
        textareaRef.current?.focus();
      },
    }));

    return (
      <div
        className={`relative flex h-full w-full bg-background font-mono text-sm select-text overflow-hidden ${className}`}
      >
        {/* Line numbers gutter */}
        <div
          ref={lineNumbersRef}
          style={{ width: `${gutterWidth}px` }}
          className="shrink-0 py-3 select-none text-right pr-3 text-muted-foreground/40 border-r border-border/50 overflow-hidden font-mono text-sm leading-6"
          onWheel={(e) => {
            if (textareaRef.current) {
              textareaRef.current.scrollTop += e.deltaY;
            }
          }}
        >
          {lineNumbers.map((num) => (
            <div key={num} className="h-6 leading-6 text-right">
              {num}
            </div>
          ))}
        </div>

        {/* Source Textarea */}
        <div className="relative flex-1 h-full min-w-0">
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onScroll={handleScroll}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            spellCheck={false}
            style={{
              fontFamily: noteFontFamily || undefined,
              fontSize: noteFontSize ? `${noteFontSize}px` : undefined,
            }}
            className="w-full h-full py-3 px-4 bg-transparent text-foreground resize-none outline-none font-mono text-sm leading-6 whitespace-pre overflow-auto"
          />
        </div>
      </div>
    );
  },
);

NoteSourceEditor.displayName = "NoteSourceEditor";
