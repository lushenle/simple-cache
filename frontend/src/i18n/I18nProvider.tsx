import { createContext, useContext, useState, useCallback, type ReactNode } from 'react';
import { translations, type Locale, type TranslationKey } from './translations';

interface I18nContextType {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: (key: TranslationKey, fallback?: string) => string;
}

const I18nContext = createContext<I18nContextType>({
  locale: 'en',
  setLocale: () => {},
  t: (key, fallback) => fallback ?? key,
});

function detectLocale(): Locale {
  const stored = localStorage.getItem('locale');
  if (stored === 'en' || stored === 'zh') return stored;
  const nav = navigator.language.toLowerCase();
  return nav.startsWith('zh') ? 'zh' : 'en';
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(detectLocale);

  const setLocale = useCallback((l: Locale) => {
    localStorage.setItem('locale', l);
    setLocaleState(l);
  }, []);

  const t = useCallback(
    (key: TranslationKey, fallback?: string) => {
      const dict = translations[locale];
      return dict[key] ?? fallback ?? key;
    },
    [locale],
  );

  return (
    <I18nContext.Provider value={{ locale, setLocale, t }}>
      {children}
    </I18nContext.Provider>
  );
}

export function useI18n() {
  return useContext(I18nContext);
}
