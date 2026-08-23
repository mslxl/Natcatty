import React, { useEffect, useImperativeHandle, useRef, useState } from "react";
import { type MarkdownActionType, wrapMarkdownSyntax } from "../../domain/notes";

export interface NoteSourceEditorHandle {
  insertAction: (action: MarkdownActionType) => void;
  focus: () => void;
}

export interface NoteSourceEditorProps {
  noteId?: string;
  value: string;
  placeholder?: string;
  onChange: (value: string) => void;
  className?: string;
  noteFontFamily?: string;
  noteFontSize?: number;
}

export const NoteSourceEditor = React.forwardRef<NoteSourceEditorHandle, NoteSourceEditorProps>(
  ({ noteId, value, placeholder = "", onChange, className = "", noteFontFamily, noteFontSize }, ref) => {
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const lineNumbersRef = useRef<HTMLDivElement>(null);
    const [localValue, setLocalValue] = useState(value);
    const prevNoteIdRef = useRef(noteId);
    const lastSyncedValueRef = useRef(value);

    // Undo/redo history for the source textarea (native textarea undo is
    // unreliable once the value is controlled by React).
    const undoStackRef = useRef<string[]>([]);
    const redoStackRef = useRef<string[]>([]);
    // Tracks whether the latest value change originated from a user edit
    // (toolbar action / Tab) so external value swaps (e.g. switching notes)
    // can reset the history stacks instead of leaking into the next note.
    const userEditRef = useRef(false);

    // Synchronize localValue when noteId switches or when external value changes
    useEffect(() => {
      if (noteId !== prevNoteIdRef.current) {
        prevNoteIdRef.current = noteId;
        setLocalValue(value);
        lastSyncedValueRef.current = value;
        undoStackRef.current = [];
        redoStackRef.current = [];
        userEditRef.current = false;
        return;
      }
      if (value !== lastSyncedValueRef.current && value !== localValue) {
        setLocalValue(value);
        lastSyncedValueRef.current = value;
        if (!userEditRef.current) {
          undoStackRef.current = [];
          redoStackRef.current = [];
        }
        userEditRef.current = false;
      }
    }, [noteId, value, localValue]);

    const lineCount = (localValue.match(/\n/g)?.length || 0) + 1;
    const lineNumbers = Array.from({ length: lineCount }, (_, i) => i + 1);
    const gutterWidth = Math.max(48, String(lineCount).length * 9 + 24);

    const handleScroll = () => {
      if (textareaRef.current && lineNumbersRef.current) {
        lineNumbersRef.current.scrollTop = textareaRef.current.scrollTop;
      }
    };

    const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      const nextValue = e.target.value;
      userEditRef.current = true;
      setLocalValue(nextValue);
      lastSyncedValueRef.current = nextValue;
      onChange(nextValue);
    };

    const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      const textarea = textareaRef.current;
      if (!textarea) return;

      // Handle Tab insertion
      if (e.key === "Tab") {
        e.preventDefault();
        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        const nextValue = `${localValue.substring(0, start)}  ${localValue.substring(end)}`;
        undoStackRef.current.push(localValue);
        redoStackRef.current = [];
        userEditRef.current = true;
        setLocalValue(nextValue);
        lastSyncedValueRef.current = nextValue;
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
            redoStackRef.current.push(localValue);
          } else {
            undoStackRef.current.push(localValue);
          }
          userEditRef.current = true;
          setLocalValue(target);
          lastSyncedValueRef.current = target;
          onChange(target);
          requestAnimationFrame(() => {
            textarea.focus();
            const curPos = Math.min(textarea.selectionStart, target.length);
            textarea.setSelectionRange(curPos, curPos);
          });
          return;
        }

        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        const result = wrapMarkdownSyntax(localValue, start, end, action);
        if (result.text !== localValue) {
          undoStackRef.current.push(localValue);
          redoStackRef.current = [];
        }
        userEditRef.current = true;
        setLocalValue(result.text);
        lastSyncedValueRef.current = result.text;
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
            value={localValue}
            onChange={handleChange}
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
