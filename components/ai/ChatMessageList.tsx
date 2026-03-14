/**
 * ChatMessageList - Renders the list of chat messages
 *
 * Claude-Code-style: user messages in bordered bubbles (right-aligned),
 * assistant responses as plain text (left-aligned, no border/bg).
 * No avatars. Thinking blocks are collapsible.
 */

import React from 'react';
import type { ChatMessage } from '../../infrastructure/ai/types';
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '../ai-elements/conversation';
import { Message, MessageContent, MessageResponse } from '../ai-elements/message';
import { ToolCall } from '../ai-elements/tool-call';
import ThinkingBlock from './ThinkingBlock';

interface ChatMessageListProps {
  messages: ChatMessage[];
  isStreaming?: boolean;
}

const ChatMessageList: React.FC<ChatMessageListProps> = ({ messages, isStreaming }) => {
  const visibleMessages = messages.filter(m => m.role !== 'system');

  if (visibleMessages.length === 0 && !isStreaming) {
    return (
      <div className="flex-1 flex items-center justify-center px-6">
        <p className="text-[13px] text-muted-foreground/40 text-center">
          Ask about your servers, run commands, or get help with configurations.
        </p>
      </div>
    );
  }

  const lastAssistantMessage = visibleMessages.findLast(m => m.role === 'assistant');

  return (
    <Conversation className="flex-1">
      <ConversationContent className="gap-1.5 px-4 py-2">
        {visibleMessages.map((message) => {
          if (message.role === 'tool') {
            return message.toolResults?.map((tr) => (
              <ToolCall
                key={tr.toolCallId}
                name={tr.toolCallId}
                result={tr.content}
                isError={tr.isError}
              />
            ));
          }

          const isUser = message.role === 'user';
          const isLastAssistant = message === lastAssistantMessage;
          const isThisStreaming = isStreaming && isLastAssistant;

          return (
            <Message key={message.id} from={message.role}>
              <MessageContent>
                {/* Thinking block */}
                {!isUser && message.thinking && (
                  <ThinkingBlock
                    content={message.thinking}
                    isStreaming={!!isThisStreaming && !message.content}
                    durationMs={message.thinkingDurationMs}
                  />
                )}

                {message.content && (
                  isUser
                    ? <div className="whitespace-pre-wrap break-words text-[13px]">{message.content}</div>
                    : <MessageResponse isAnimating={isThisStreaming}>
                        {message.content}
                      </MessageResponse>
                )}

                {/* Tool calls */}
                {message.toolCalls?.map((tc) => (
                  <ToolCall
                    key={tc.id}
                    name={tc.name}
                    args={tc.arguments}
                    isLoading={message.executionStatus === 'running'}
                  />
                ))}
              </MessageContent>
            </Message>
          );
        })}

        {/* Streaming indicator — only when no content and no thinking yet */}
        {isStreaming && !lastAssistantMessage?.content && !lastAssistantMessage?.thinking && (
          <div className="flex items-center gap-1 py-2">
            <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/30 animate-bounce [animation-delay:0ms]" />
            <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/30 animate-bounce [animation-delay:150ms]" />
            <span className="w-1.5 h-1.5 rounded-full bg-muted-foreground/30 animate-bounce [animation-delay:300ms]" />
          </div>
        )}
      </ConversationContent>
      <ConversationScrollButton />
    </Conversation>
  );
};

export default React.memo(ChatMessageList);
