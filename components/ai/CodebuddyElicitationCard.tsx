import React, { useMemo, useState } from 'react';
import { MessageCircleQuestion } from 'lucide-react';
import { useI18n } from '../../application/i18n/I18nProvider';
import type {
  CodebuddyElicitation,
  CodebuddyElicitationAction,
} from '../../infrastructure/ai/shared/codebuddyElicitations';
import { Button } from '../ui/button';

interface ElicitationField {
  id: string;
  title: string;
  description: string;
  type: string;
  required: boolean;
  defaultValue?: unknown;
  options: Array<{ value: string; label: string }>;
  minimum?: number;
  maximum?: number;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
}

function fieldOptions(schema: Record<string, unknown>): Array<{ value: string; label: string }> {
  if (Array.isArray(schema.enum)) {
    const labels = Array.isArray(schema.enumNames) ? schema.enumNames : [];
    return schema.enum.map((value, index) => ({
      value: String(value),
      label: String(labels[index] ?? value),
    }));
  }
  const variants = Array.isArray(schema.oneOf)
    ? schema.oneOf
    : Array.isArray(schema.anyOf)
      ? schema.anyOf
      : [];
  return variants.flatMap((variant) => {
    const option = asRecord(variant);
    return option.const == null
      ? []
      : [{ value: String(option.const), label: String(option.title ?? option.const) }];
  });
}

function parseFields(elicitation: CodebuddyElicitation): ElicitationField[] {
  const schema = asRecord(elicitation.request.requestedSchema);
  const properties = asRecord(schema.properties);
  const required = new Set(
    Array.isArray(schema.required) ? schema.required.map((value) => String(value)) : [],
  );
  return Object.entries(properties).map(([id, rawField]) => {
    const field = asRecord(rawField);
    const type = String(field.type || 'string');
    return {
      id,
      title: String(field.title || id),
      description: String(field.description || ''),
      type,
      required: required.has(id),
      defaultValue: field.default,
      options: fieldOptions(type === 'array' ? asRecord(field.items) : field),
      minimum: typeof field.minimum === 'number' ? field.minimum : undefined,
      maximum: typeof field.maximum === 'number' ? field.maximum : undefined,
    };
  });
}

function initialValues(fields: ElicitationField[]): Record<string, unknown> {
  return Object.fromEntries(
    fields
      .filter((field) => field.defaultValue !== undefined)
      .map((field) => [field.id, field.defaultValue]),
  );
}

export const CodebuddyElicitationCard: React.FC<{
  elicitation: CodebuddyElicitation;
  onRespond: (
    action: CodebuddyElicitationAction,
    content?: Record<string, unknown>,
  ) => Promise<void>;
}> = ({ elicitation, onRespond }) => {
  const { t } = useI18n();
  const fields = useMemo(() => parseFields(elicitation), [elicitation]);
  const [values, setValues] = useState<Record<string, unknown>>(() => initialValues(fields));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const complete = fields.every((field) => {
    if (!field.required) return true;
    const value = values[field.id];
    if (field.type === 'boolean') return typeof value === 'boolean';
    if (field.type === 'array') return Array.isArray(value) && value.length > 0;
    return value !== undefined && String(value).trim().length > 0;
  });

  const respond = async (
    action: CodebuddyElicitationAction,
    content?: Record<string, unknown>,
  ) => {
    if (submitting) return;
    setSubmitting(true);
    setError('');
    try {
      await onRespond(action, content);
    } catch (responseError) {
      setError(responseError instanceof Error ? responseError.message : String(responseError));
      setSubmitting(false);
    }
  };

  return (
    <div className="rounded-lg border border-blue-500/30 bg-card/70 p-3 space-y-3">
      <div className="flex items-start gap-2">
        <MessageCircleQuestion size={16} className="mt-0.5 shrink-0 text-blue-500" />
        <div className="min-w-0">
          <div className="text-sm font-medium">{t('ai.codebuddy.elicitation.title')}</div>
          <div className="text-xs text-muted-foreground leading-5">
            {elicitation.request.message || t('ai.codebuddy.elicitation.description')}
          </div>
        </div>
      </div>

      {fields.map((field) => (
        <div key={field.id} className="block space-y-1.5">
          <span className="text-xs font-medium">
            {field.title}{field.required ? ' *' : ''}
          </span>
          {field.description ? (
            <span className="block text-[11px] text-muted-foreground">{field.description}</span>
          ) : null}
          {field.type === 'array' && field.options.length > 0 ? (
            <div className="space-y-1.5">
              {field.options.map((option) => {
                const selected = Array.isArray(values[field.id])
                  ? values[field.id] as unknown[]
                  : [];
                return (
                  <label
                    key={option.value}
                    className="flex items-center gap-2 rounded-md border border-border/50 px-2.5 py-2 text-xs"
                  >
                    <input
                      type="checkbox"
                      checked={selected.map(String).includes(option.value)}
                      disabled={submitting}
                      onChange={(event) => {
                        const next = event.target.checked
                          ? [...selected, option.value]
                          : selected.filter((value) => String(value) !== option.value);
                        setValues((current) => ({ ...current, [field.id]: next }));
                      }}
                    />
                    <span>{option.label}</span>
                  </label>
                );
              })}
            </div>
          ) : field.options.length > 0 ? (
            <select
              value={String(values[field.id] ?? '')}
              disabled={submitting}
              onChange={(event) => setValues((current) => ({
                ...current,
                [field.id]: event.target.value,
              }))}
              className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-xs outline-none focus-visible:ring-1 focus-visible:ring-ring"
            >
              <option value="">{t('ai.codebuddy.elicitation.select')}</option>
              {field.options.map((option) => (
                <option key={option.value} value={option.value}>{option.label}</option>
              ))}
            </select>
          ) : field.type === 'boolean' ? (
            <select
              value={values[field.id] === undefined ? '' : String(values[field.id])}
              disabled={submitting}
              onChange={(event) => {
                const value = event.target.value === ''
                  ? undefined
                  : event.target.value === 'true';
                setValues((current) => ({ ...current, [field.id]: value }));
              }}
              className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-xs outline-none focus-visible:ring-1 focus-visible:ring-ring"
            >
              <option value="">{t('ai.codebuddy.elicitation.select')}</option>
              <option value="true">{t('ai.codebuddy.elicitation.yes')}</option>
              <option value="false">{t('ai.codebuddy.elicitation.no')}</option>
            </select>
          ) : (
            <input
              type={field.type === 'number' || field.type === 'integer' ? 'number' : 'text'}
              min={field.minimum}
              max={field.maximum}
              step={field.type === 'integer' ? 1 : undefined}
              value={String(values[field.id] ?? '')}
              disabled={submitting}
              onChange={(event) => {
                const raw = event.target.value;
                const value = field.type === 'number' || field.type === 'integer'
                  ? (raw === '' ? undefined : Number(raw))
                  : raw;
                setValues((current) => ({ ...current, [field.id]: value }));
              }}
              className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-xs outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
          )}
        </div>
      ))}

      {error ? <p className="text-xs text-destructive">{error}</p> : null}

      <div className="flex flex-wrap justify-end gap-2">
        <Button
          variant="ghost"
          size="sm"
          disabled={submitting}
          onClick={() => void respond('cancel')}
        >
          {t('common.cancel')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={submitting}
          onClick={() => void respond('decline')}
        >
          {t('ai.codebuddy.elicitation.decline')}
        </Button>
        <Button
          size="sm"
          disabled={submitting || !complete}
          onClick={() => void respond('accept', Object.fromEntries(
            Object.entries(values).filter(([, value]) => value !== undefined),
          ))}
        >
          {t('ai.codebuddy.elicitation.accept')}
        </Button>
      </div>
    </div>
  );
};
