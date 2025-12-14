import { init as initGhostty } from 'ghostty-web';
import ReactDOM from 'react-dom/client';
import App from './App';
import SettingsPage from './components/SettingsPage';
import { ToastProvider } from './components/ui/toast';

// Pre-load Ghostty WASM immediately on app start for faster terminal open
initGhostty().catch((err) => {
  console.warn('[Ghostty] WASM preload failed, will retry on terminal open:', err);
});

const rootElement = document.getElementById('root');
if (!rootElement) {
  throw new Error("Could not find root element to mount to");
}

// Simple hash-based routing for separate windows
const getRoute = () => {
  const hash = window.location.hash;
  if (hash === '#/settings' || hash.startsWith('#/settings')) {
    return 'settings';
  }
  return 'main';
};

const root = ReactDOM.createRoot(rootElement);

const renderApp = () => {
  const route = getRoute();
  if (route === 'settings') {
    root.render(
      <ToastProvider>
        <SettingsPage />
      </ToastProvider>
    );
  } else {
    root.render(<App />);
  }
};

// Initial render
renderApp();

// Listen for hash changes
window.addEventListener('hashchange', renderApp);
