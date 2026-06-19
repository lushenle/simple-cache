import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react';
import { hasToken, setToken, clearToken } from '../api/client';

interface AuthContextType {
  authenticated: boolean;
  checking: boolean;
  login: (token: string) => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextType>({
  authenticated: false,
  checking: true,
  login: () => {},
  logout: () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
  const [authenticated, setAuthenticated] = useState(false);
  const [checking, setChecking] = useState(true);

  // On mount, check if the server requires auth.
  // If auth is disabled, auto-login without a token.
  useEffect(() => {
    let cancelled = false;
    fetch('/admin/api/config')
      .then((r) => r.json())
      .then((cfg: { auth_enabled?: boolean }) => {
        if (cancelled) return;
        if (!cfg.auth_enabled) {
          // Auth not required — auto-login.
          setAuthenticated(true);
        } else if (hasToken()) {
          // Auth required and we already have a token — try it.
          setAuthenticated(true);
        }
        setChecking(false);
      })
      .catch(() => {
        if (!cancelled) {
          // Can't reach the config endpoint — treat as no auth for local dev.
          setAuthenticated(true);
          setChecking(false);
        }
      });
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    const handler = () => setAuthenticated(false);
    window.addEventListener('auth:expired', handler);
    return () => window.removeEventListener('auth:expired', handler);
  }, []);

  const login = useCallback((token: string) => {
    setToken(token);
    setAuthenticated(true);
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setAuthenticated(false);
  }, []);

  return (
    <AuthContext.Provider value={{ authenticated, checking, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  return useContext(AuthContext);
}
