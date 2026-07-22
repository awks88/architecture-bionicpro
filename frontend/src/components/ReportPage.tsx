import React, { useEffect, useState } from 'react';

type AuthStatus = 'checking' | 'authenticated' | 'anonymous';

interface SessionResponse {
  authenticated: boolean;
  username?: string;
}

interface ReportLinkResponse {
  download_url: string;
  cached: boolean;
}

const authBaseURL = process.env.REACT_APP_API_URL || 'http://localhost:8000';

const yesterday = () => {
  const value = new Date();
  value.setUTCDate(value.getUTCDate() - 1);
  return value.toISOString().slice(0, 10);
};

const ReportPage: React.FC = () => {
  const [authStatus, setAuthStatus] = useState<AuthStatus>('checking');
  const [username, setUsername] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [fromDate, setFromDate] = useState(yesterday());
  const [toDate, setToDate] = useState(yesterday());

  useEffect(() => {
    const controller = new AbortController();

    const checkSession = async () => {
      try {
        const response = await fetch(`${authBaseURL}/auth/session`, {
          credentials: 'include',
          signal: controller.signal,
        });

        if (response.status === 401) {
          setAuthStatus('anonymous');
          return;
        }
        if (!response.ok) {
          throw new Error(`Session check failed: ${response.status}`);
        }

        const session: SessionResponse = await response.json();
        setUsername(session.username || null);
        setAuthStatus(session.authenticated ? 'authenticated' : 'anonymous');
      } catch (err) {
        if (err instanceof DOMException && err.name === 'AbortError') {
          return;
        }
        setError(err instanceof Error ? err.message : 'Session check failed');
        setAuthStatus('anonymous');
      }
    };

    checkSession();
    return () => controller.abort();
  }, []);

  const login = () => {
    const returnTo = encodeURIComponent(window.location.href);
    window.location.assign(`${authBaseURL}/auth/login?return_to=${returnTo}`);
  };

  const logout = async () => {
    setError(null);
    try {
      const response = await fetch(`${authBaseURL}/auth/logout`, {
        method: 'POST',
        credentials: 'include',
      });
      if (!response.ok && response.status !== 401) {
        throw new Error(`Logout failed: ${response.status}`);
      }
      setUsername(null);
      setAuthStatus('anonymous');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Logout failed');
    }
  };

  const downloadReport = async () => {
    try {
      setLoading(true);
      setError(null);
      setMessage(null);

      const params = new URLSearchParams({ from: fromDate, to: toDate });
      const response = await fetch(`${authBaseURL}/reports?${params}`, {
        credentials: 'include',
      });

      if (response.status === 401) {
        setAuthStatus('anonymous');
        throw new Error('Session expired. Please sign in again.');
      }
      if (!response.ok) {
        const body = await response.text();
        throw new Error(body || `Report request failed: ${response.status}`);
      }

      const report: ReportLinkResponse = await response.json();
      const link = document.createElement('a');
      link.href = report.download_url;
      link.download = `bionicpro-report-${fromDate}-${toDate}.json`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      setMessage(report.cached ? 'Report loaded from S3 cache' : 'Report generated and saved to S3');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'An error occurred');
    } finally {
      setLoading(false);
    }
  };

  if (authStatus === 'checking') {
    return <div>Loading...</div>;
  }

  if (authStatus === 'anonymous') {
    return (
      <div className="flex flex-col items-center justify-center min-h-screen bg-gray-100">
        <button
          onClick={login}
          className="px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
        >
          Login
        </button>
        {error && (
          <div className="mt-4 p-4 bg-red-100 text-red-700 rounded">
            {error}
          </div>
        )}

      </div>
    );
  }

  return (
    <div className="flex flex-col items-center justify-center min-h-screen bg-gray-100">
      <div className="p-8 bg-white rounded-lg shadow-md">
        <h1 className="text-2xl font-bold mb-6">Usage Reports</h1>
        {username && <p className="mb-4 text-gray-600">Signed in as {username}</p>}

        <div className="flex gap-3 mb-4">
          <label className="flex flex-col text-sm text-gray-600">
            From
            <input
              type="date"
              value={fromDate}
              onChange={(event) => setFromDate(event.target.value)}
              className="mt-1 border rounded px-2 py-1"
            />
          </label>
          <label className="flex flex-col text-sm text-gray-600">
            To
            <input
              type="date"
              value={toDate}
              onChange={(event) => setToDate(event.target.value)}
              className="mt-1 border rounded px-2 py-1"
            />
          </label>
        </div>

        <button
          onClick={downloadReport}
          disabled={loading}
          className={`px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600 ${
            loading ? 'opacity-50 cursor-not-allowed' : ''
          }`}
        >
          {loading ? 'Generating Report...' : 'Download Report'}
        </button>

        <button
          onClick={logout}
          className="ml-3 px-4 py-2 bg-gray-500 text-white rounded hover:bg-gray-600"
        >
          Logout
        </button>

        {error && (
          <div className="mt-4 p-4 bg-red-100 text-red-700 rounded">
            {error}
          </div>
        )}

        {message && (
          <div className="mt-4 p-4 bg-green-100 text-green-700 rounded">
            {message}
          </div>
        )}
      </div>
    </div>
  );
};

export default ReportPage;
