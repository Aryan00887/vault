import { useCallback, useEffect, useRef, useState } from 'react';
import { LogOut } from 'lucide-react';
import Login from './components/Login';
import Sidebar from './components/Sidebar';
import type { Tab } from './components/Sidebar';
import Overview from './components/Overview';
import Nodes from './components/Nodes';
import Policy from './components/Policy';
import Objects from './components/Objects';
import Toaster from './components/Toaster';
import { ClusterStatusPill } from './components/StatusPill';
import { ApiError, getOverview } from './api';
import type { Overview as OverviewData } from './types';
import { useToasts } from './hooks/useToasts';

const TOKEN_KEY = 'vault:token';
const POLL_INTERVAL_MS = 4000;

const TAB_TITLES: Record<Tab, string> = {
  overview: 'Overview',
  objects: 'Objects',
  nodes: 'Nodes',
  policy: 'Policy',
};

export default function App() {
  const [token, setToken] = useState<string | null>(() => window.localStorage.getItem(TOKEN_KEY));
  const [overview, setOverview] = useState<OverviewData | null>(null);
  const [overviewError, setOverviewError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>('overview');
  const { toasts, push, dismiss } = useToasts();
  const pollRef = useRef<number | null>(null);

  const logout = useCallback(
    (message?: string) => {
      window.localStorage.removeItem(TOKEN_KEY);
      setToken(null);
      setOverview(null);
      if (message) push('error', message);
    },
    [push]
  );

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const data = await getOverview(token);
      setOverview(data);
      setOverviewError(null);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        logout('Session expired — reconnect with a valid operator token.');
        return;
      }
      setOverviewError(err instanceof Error ? err.message : 'Could not reach the Vault API.');
    }
  }, [token, logout]);

  useEffect(() => {
    if (!token) return;
    refresh();
    pollRef.current = window.setInterval(refresh, POLL_INTERVAL_MS);
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current);
    };
  }, [token, refresh]);

  function handleConnect(newToken: string, initialOverview: OverviewData) {
    window.localStorage.setItem(TOKEN_KEY, newToken);
    setToken(newToken);
    setOverview(initialOverview);
    setOverviewError(null);
  }

  if (!token) {
    return <Login onConnect={handleConnect} />;
  }

  return (
    <div className="shell">
      <Sidebar tab={tab} onSelect={setTab} />
      <div className="shell__main">
        <header className="topbar">
          <h1 className="topbar__title">{TAB_TITLES[tab]}</h1>
          <div className="topbar__right">
            {overview && <ClusterStatusPill state={overview.cluster.state} pulse />}
            <button className="btn btn--ghost btn--small" onClick={() => logout()}>
              <LogOut size={15} strokeWidth={1.75} />
              Disconnect
            </button>
          </div>
        </header>
        <main className="content">
          {tab === 'overview' && (
            <Overview overview={overview} error={overviewError} onRetry={refresh} />
          )}
          {tab === 'objects' && (
            <Objects
              token={token}
              pushToast={push}
              onChanged={refresh}
              onUnauthorized={() => logout('Session expired — reconnect with a valid operator token.')}
            />
          )}
          {tab === 'nodes' && (
            <Nodes
              overview={overview}
              token={token}
              onChanged={refresh}
              pushToast={push}
              onUnauthorized={() => logout('Session expired — reconnect with a valid operator token.')}
            />
          )}
          {tab === 'policy' && (
            <Policy
              overview={overview}
              token={token}
              onChanged={refresh}
              pushToast={push}
              onUnauthorized={() => logout('Session expired — reconnect with a valid operator token.')}
            />
          )}
        </main>
      </div>
      <Toaster toasts={toasts} onDismiss={dismiss} />
    </div>
  );
}
