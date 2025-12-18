import ReactDOM from 'react-dom/client';
import '@xterm/xterm/css/xterm.css';
import App from './App';
import SettingsPage from './components/SettingsPage';
import { ToastProvider } from './components/ui/toast';

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
