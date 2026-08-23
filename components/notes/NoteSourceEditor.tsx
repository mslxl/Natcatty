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
    const prevValueRef = useRef(value);

    // Undo/redo history for the source textarea (native textarea undo is
    // unreliable once the value is controlled by React).
    const undoStackRef = useRef<string[]>([]);
    const redoStackRef = useRef<string[]>([]);

    // Adopt the external value only for genuine external changes:
    // - noteId switch → always adopt (new note).
    // - prop value changed AND differs from localValue → external edit.
    // The parent debounces our own edits and echoes them back with
    // value === localValue; those echoes must NOT reset the textarea, or the
    // keystroke would be reverted and the caret would jump to the end.
    useEffect(() => {
      if (noteId !== prevNoteIdRef.current) {
        prevNoteIdRef.current = noteId;
        prevValueRef.current = value;
        setLocalValue(value);
        undoStackRef.current = [];
        redoStackRef.current = [];
        return;
      }
      if (value !== prevValueRef.current) {
        prevValueRef.current = value;
        if (value !== localValue) {
          setLocalValue(value);
          undoStackRef.current = [];
          redoStackRef.current = [];
        }
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
      setLocalValue(nextValue);
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
        setLocalValue(nextValue);
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
          setLocalValue(target);
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
        setLocalValue(result.text);
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
