import React, { useImperativeHandle, useRef } from "react";
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
}

export const NoteSourceEditor = React.forwardRef<NoteSourceEditorHandle, NoteSourceEditorProps>(
  ({ value, placeholder = "", onChange, className = "" }, ref) => {
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const lineNumbersRef = useRef<HTMLDivElement>(null);

    const lineCount = (value.match(/\n/g)?.length || 0) + 1;
    const lineNumbers = Array.from({ length: lineCount }, (_, i) => i + 1);

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
        const start = textarea.selectionStart;
        const end = textarea.selectionEnd;
        const result = wrapMarkdownSyntax(value, start, end, action);
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
          className="shrink-0 w-12 py-4 select-none text-right pr-3 text-muted-foreground/40 border-r border-border/50 overflow-hidden text-xs font-mono leading-6"
        >
          {lineNumbers.map((num) => (
            <div key={num}>{num}</div>
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
            className="w-full h-full p-4 bg-transparent text-foreground resize-none outline-none font-mono text-sm leading-6 whitespace-pre-wrap break-words overflow-y-auto"
          />
        </div>
      </div>
    );
  },
);

NoteSourceEditor.displayName = "NoteSourceEditor";
