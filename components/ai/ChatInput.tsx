/**
 * ChatInput - Zed-style bottom input area for the AI chat panel
 *
 * Thin wrapper around the AI Elements prompt-input components.
 * Bordered textarea with monospace placeholder, expand toggle,
 * and a bottom toolbar with muted controls + subtle send button.
 */

import { Check, ChevronDown, ChevronRight, Cpu, Expand, Plus } from 'lucide-react';
import React, { useCallback, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { FormEvent } from 'react';
import {
  PromptInput,
  PromptInputButton,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from '../ai-elements/prompt-input';
import type { PromptInputStatus } from '../ai-elements/prompt-input';
import { formatThinkingLabel } from '../../infrastructure/ai/types';
import type { AgentModelPreset } from '../../infrastructure/ai/types';

interface ChatInputProps {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  onStop?: () => void;
  isStreaming?: boolean;
  disabled?: boolean;
  providerName?: string;
  modelName?: string;
  agentName?: string;
  placeholder?: string;
  /** Available model presets for the current agent */
  modelPresets?: AgentModelPreset[];
  /** Currently selected model ID */
  selectedModelId?: string;
  /** Callback when user selects a model */
  onModelSelect?: (modelId: string) => void;
}

const ChatInput: React.FC<ChatInputProps> = ({
  value,
  onChange,
  onSend,
  onStop,
  isStreaming = false,
  disabled = false,
  providerName,
  modelName,
  agentName,
  placeholder,
  modelPresets = [],
  selectedModelId,
  onModelSelect,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [showModelPicker, setShowModelPicker] = useState(false);
  const [pickerPos, setPickerPos] = useState<{ left: number; bottom: number } | null>(null);
  const [hoveredModelId, setHoveredModelId] = useState<string | null>(null);
  const modelBtnRef = useRef<HTMLButtonElement>(null);

  const defaultPlaceholder = agentName
    ? `Message ${agentName} — @ to include context, / for commands`
    : 'Message Catty Agent...';

  const handleSubmit = useCallback(
    (_text: string, _event: FormEvent<HTMLFormElement>) => {
      onSend();
    },
    [onSend],
  );

  const status: PromptInputStatus = isStreaming ? 'streaming' : 'idle';

  // Permission mode chip removed — agents run in autonomous mode

  // selectedModelId may be "model/thinking" for codex
  const selectedBaseModelId = selectedModelId?.split('/')[0];
  const selectedThinking = selectedModelId?.includes('/') ? selectedModelId.split('/')[1] : undefined;
  const selectedPreset = modelPresets.find(m => m.id === selectedBaseModelId);
  const modelLabel = selectedPreset
    ? selectedPreset.name + (selectedThinking ? ` / ${formatThinkingLabel(selectedThinking)}` : '')
    : modelName || providerName || 'No model';
  const hasModelPicker = modelPresets.length > 0 && onModelSelect;
  const chipClassName =
    'inline-flex h-6 items-center gap-1 rounded-full px-1.5 text-[10.5px] text-foreground/72';
  const iconButtonClassName =
    'h-6 w-6 rounded-full bg-transparent text-foreground/62 hover:bg-muted/24 hover:text-foreground';

  return (
    <div className="shrink-0 px-4 pb-4">
      <PromptInput onSubmit={handleSubmit}>
        {/* Textarea with expand toggle */}
        <div className="relative">
          <PromptInputTextarea
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={placeholder || defaultPlaceholder}
            disabled={disabled || isStreaming}
            className={expanded ? 'max-h-[220px]' : undefined}
          />
          <button
            type="button"
            onClick={() => setExpanded((e) => !e)}
            className="absolute top-3.5 right-3 rounded-md p-1 text-muted-foreground/38 hover:text-muted-foreground/72 hover:bg-muted/25 transition-colors cursor-pointer"
            title={expanded ? 'Collapse' : 'Expand'}
          >
            <Expand size={12} />
          </button>
        </div>

        {/* Footer toolbar */}
        <PromptInputFooter className="gap-1.5 border-t-0 bg-transparent px-3 pb-2 pt-0">
          <PromptInputTools className="gap-1 flex-wrap">
            <PromptInputButton tooltip="Attach context" className={iconButtonClassName}>
              <Plus size={13} />
            </PromptInputButton>
            <button
              ref={modelBtnRef}
              type="button"
              onClick={() => {
                if (!hasModelPicker) return;
                if (!showModelPicker) {
                  const rect = modelBtnRef.current?.getBoundingClientRect();
                  if (rect) setPickerPos({ left: rect.left, bottom: window.innerHeight - rect.top + 6 });
                }
                setShowModelPicker(v => !v);
              }}
              className={`${chipClassName} ${hasModelPicker ? 'cursor-pointer hover:bg-muted/24 transition-colors' : ''}`}
            >
              <Cpu size={11} className="text-muted-foreground/64" />
              <span className="truncate max-w-[82px]">{modelLabel}</span>
              {hasModelPicker && <ChevronDown size={9} className="text-muted-foreground/50" />}
            </button>
            {showModelPicker && hasModelPicker && pickerPos && createPortal(
              <>
                <div className="fixed inset-0 z-[999]" onClick={() => { setShowModelPicker(false); setHoveredModelId(null); }} />
                <div
                  className="fixed z-[1000] min-w-[160px] rounded-lg border border-border/50 bg-popover shadow-lg py-1"
                  style={{ left: pickerPos.left, bottom: pickerPos.bottom }}
                  onMouseLeave={() => setHoveredModelId(null)}
                >
                  {modelPresets.map(preset => {
                    const isSelected = preset.id === selectedBaseModelId;
                    const hasThinking = preset.thinkingLevels && preset.thinkingLevels.length > 0;
                    return (
                      <div key={preset.id} className="relative" onMouseEnter={() => setHoveredModelId(hasThinking ? preset.id : null)}>
                        <button
                          type="button"
                          onClick={() => {
                            if (!hasThinking) {
                              onModelSelect?.(preset.id);
                              setShowModelPicker(false);
                              setHoveredModelId(null);
                            }
                          }}
                          className="w-full flex items-center gap-1.5 px-3 py-1.5 text-left text-[12px] hover:bg-muted/30 transition-colors cursor-pointer whitespace-nowrap"
                        >
                          {isSelected ? <Check size={11} className="text-primary shrink-0" /> : <span className="w-[11px] shrink-0" />}
                          <span className="flex-1 text-foreground/85">{preset.name}</span>
                          {preset.description && <span className="text-[10px] text-muted-foreground/50 mr-1">{preset.description}</span>}
                          {hasThinking && <ChevronRight size={10} className="text-muted-foreground/50" />}
                        </button>
                        {/* Thinking level sub-menu */}
                        {hasThinking && hoveredModelId === preset.id && (
                          <div className="absolute left-full top-0 ml-1 min-w-[120px] rounded-lg border border-border/50 bg-popover shadow-lg py-1 z-[1001]">
                            {preset.thinkingLevels!.map(level => {
                              const fullId = `${preset.id}/${level}`;
                              const isLevelSelected = selectedModelId === fullId;
                              return (
                                <button
                                  key={level}
                                  type="button"
                                  onClick={() => {
                                    onModelSelect?.(fullId);
                                    setShowModelPicker(false);
                                    setHoveredModelId(null);
                                  }}
                                  className="w-full flex items-center gap-1.5 px-3 py-1.5 text-left text-[12px] hover:bg-muted/30 transition-colors cursor-pointer whitespace-nowrap"
                                >
                                  {isLevelSelected ? <Check size={11} className="text-primary shrink-0" /> : <span className="w-[11px] shrink-0" />}
                                  <span className="text-foreground/85">{formatThinkingLabel(level)}</span>
                                </button>
                              );
                            })}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              </>,
              document.body,
            )}
          </PromptInputTools>

          <div className="flex-1 min-w-0" />

          <div className="flex items-center gap-1">
            <PromptInputSubmit
              status={status}
              onStop={onStop}
              disabled={!value.trim() || disabled}
            />
          </div>
        </PromptInputFooter>
      </PromptInput>
    </div>
  );
};

export default React.memo(ChatInput);
