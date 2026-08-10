import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'
import App from './App'
import DesignSystem from './pages/DesignSystem'
import { initTheme } from './lib/theme'
import { I18nProvider } from './lib/i18n'

initTheme()

// Lightweight routing until the app needs a router: #design shows the design system.
const route = window.location.hash.replace(/^#\/?/, '')
const Root = route === 'design' ? DesignSystem : App

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
      <Root />
    </I18nProvider>
  </StrictMode>,
)
