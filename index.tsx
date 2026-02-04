import { Suspense, lazy } from 'react';
import ReactDOM from 'react-dom/client';
import '@fontsource/space-grotesk/400.css';
import '@fontsource/space-grotesk/500.css';
import '@fontsource/space-grotesk/600.css';
import '@fontsource/space-grotesk/700.css';
import '@fontsource/jetbrains-mono/400.css';
import '@fontsource/jetbrains-mono/500.css';
import '@fontsource/jetbrains-mono/600.css';
import App from './App';
import { ToastProvider } from './components/ui/toast';

const LazySettingsPage = lazy(() => import('./components/SettingsPage'));
const LazyTrayPanel = lazy(() => import('./components/TrayPanel'));

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
  if (hash === '#/tray' || hash.startsWith('#/tray')) {
    return 'tray';
  }
  return 'main';
};

const root = ReactDOM.createRoot(rootElement);

const renderApp = () => {
  const route = getRoute();
  if (route === 'settings') {
    root.render(
      <ToastProvider>
        <Suspense fallback={null}>
          <LazySettingsPage />
        </Suspense>
      </ToastProvider>
    );
  } else if (route === 'tray') {
    root.render(
      <ToastProvider>
        <Suspense fallback={null}>
          <LazyTrayPanel />
        </Suspense>
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
