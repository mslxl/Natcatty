import React, { useMemo } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Combobox, type ComboboxOption } from '../ui/combobox';

interface SelectableFont {
  id: string;
  name: string;
  family: string;
}

interface FontSelectProps {
  value: string;
  fonts: SelectableFont[];
  onChange: (value: string) => void;
  className?: string;
  disabled?: boolean;
}

export const FontSelect: React.FC<FontSelectProps> = ({
  value,
  fonts,
  onChange,
  className,
  disabled,
}) => {
  const { t } = useI18n();
  const selectedFont = fonts.find((font) => font.id === value);
  const options = useMemo<ComboboxOption[]>(() => fonts.map((font) => ({
    value: font.id,
    label: font.name,
    labelStyle: { fontFamily: font.family },
  })), [fonts]);

  return (
    <Combobox
      options={options}
      value={value}
      onValueChange={onChange}
      placeholder={t('common.searchPlaceholder')}
      emptyText={t('common.noResultsFound')}
      triggerClassName={className}
      inputStyle={{ fontFamily: selectedFont?.family }}
      disabled={disabled}
      clearable={false}
    />
  );
};

export default FontSelect;
