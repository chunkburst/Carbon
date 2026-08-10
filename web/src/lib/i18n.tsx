/* eslint-disable react-refresh/only-export-components -- This module intentionally exposes the provider, hook, and non-React translation helpers as one API. */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { syncNativeLanguage } from "@/lib/desktop";

export type Language = "en" | "zh";
type TranslationVariables = Record<string, string | number | null | undefined>;

export type Translate = (en: string, zh: string, variables?: TranslationVariables) => string;

const STORAGE_KEY = "carbon-language";
const LEGACY_STORAGE_KEY = "cairn-language";

function storedLanguage(): Language | null {
  const current = window.localStorage.getItem(STORAGE_KEY);
  if (current === "en" || current === "zh") return current;

  const legacy = window.localStorage.getItem(LEGACY_STORAGE_KEY);
  if (legacy !== "en" && legacy !== "zh") return null;
  // One-time migration: all future writes use the Carbon key only.
  window.localStorage.setItem(STORAGE_KEY, legacy);
  try {
    window.localStorage.removeItem(LEGACY_STORAGE_KEY);
  } catch {
    // The canonical value is already durable; retry legacy cleanup on a later startup.
  }
  return legacy;
}

function preferredLanguage(): Language {
  if (typeof window !== "undefined") {
    try {
      const stored = storedLanguage();
      if (stored) return stored;
    } catch {
      // Private browsing or an embedded webview can deny storage. Fall back to the browser locale.
    }
  }

  return typeof navigator !== "undefined" && navigator.language.toLowerCase().startsWith("zh")
    ? "zh"
    : "en";
}

function interpolate(text: string, variables?: TranslationVariables): string {
  if (!variables) return text;
  return text.replace(/\{([A-Za-z0-9_]+)\}/g, (placeholder, key) => {
    const value = variables[key];
    return value === null || value === undefined ? placeholder : String(value);
  });
}

function syncDocumentLanguage(language: Language) {
  if (typeof document !== "undefined") {
    document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  }
}

let activeLanguage: Language = preferredLanguage();
syncDocumentLanguage(activeLanguage);

/** Returns the current language for code that cannot use a React hook. */
export function currentLanguage(): Language {
  return activeLanguage;
}

/** Translates a compact English/Chinese pair outside React components. */
export function translate(
  en: string,
  zh: string,
  variables?: TranslationVariables,
  language: Language = activeLanguage,
): string {
  return interpolate(language === "zh" ? zh : en, variables);
}

type I18nContextValue = {
  language: Language;
  setLanguage: (language: Language) => void;
  locale: "en-US" | "zh-CN";
  t: Translate;
};

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [language, setLanguageState] = useState<Language>(() => activeLanguage);

  const setLanguage = useCallback((nextLanguage: Language) => {
    activeLanguage = nextLanguage;
    syncDocumentLanguage(nextLanguage);
    try {
      window.localStorage.setItem(STORAGE_KEY, nextLanguage);
    } catch {
      // The language still changes for this session when persistent storage is unavailable.
    }
    setLanguageState(nextLanguage);
  }, []);

  useEffect(() => {
    activeLanguage = language;
    syncDocumentLanguage(language);
    void syncNativeLanguage(language);
  }, [language]);

  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== STORAGE_KEY || (event.newValue !== "en" && event.newValue !== "zh")) return;
      activeLanguage = event.newValue;
      syncDocumentLanguage(event.newValue);
      setLanguageState(event.newValue);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const value = useMemo<I18nContextValue>(
    () => ({
      language,
      setLanguage,
      locale: language === "zh" ? "zh-CN" : "en-US",
      t: (en, zh, variables) => interpolate(language === "zh" ? zh : en, variables),
    }),
    [language, setLanguage],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const context = useContext(I18nContext);
  if (!context) throw new Error("useI18n must be used within I18nProvider");
  return context;
}
