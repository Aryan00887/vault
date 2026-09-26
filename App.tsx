import { useRef, useState } from 'react';
import type { ChangeEvent } from 'react';
import { ApiError, deleteObject, downloadObject, headObject, putObject } from './api';
import type { ObjectHead } from './api';
import { formatBytes, shortHash } from './format';

const TOKEN_KEY = 'vault:token';

type Busy = null | 'check' | 'upload' | 'download' | 'delete';
type Message = { kind: 'success' | 'error' | 'info'; text: string };

export default function App() {
  const [token, setToken] = useState(() => window.localStorage.getItem(TOKEN_KEY) ?? '');
  const [key, setKey] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState<Busy>(null);
  const [progress, setProgress] = useState(0);
  const [message, setMessage] = useState<Message | null>(null);
  const [metadata, setMetadata] = useState<ObjectHead | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  function rememberToken(value: string) {
    setToken(value);
    window.localStorage.setItem(TOKEN_KEY, value);
  }

  function onFileChange(e: ChangeEvent<HTMLInputElement>) {
    const picked = e.target.files?.[0] ?? null;
    setFile(picked);
    if (picked && !key.trim()) setKey(picked.name);
  }

  function reportError(err: unknown, fallback: string) {
    if (err instanceof ApiError && err.status === 401) {
      setMessage({ kind: 'error', text: 'That token was rejected. Check the token and try again.' });
      return;
    }
    setMessage({ kind: 'error', text: err instanceof Error ? err.message : fallback });
  }

  async function handleCheck() {
    const trimmedKey = key.trim();
    if (!token.trim() || !trimmedKey) return;
    setBusy('check');
    setMetadata(null);
    setMessage(null);
    setConfirmDelete(false);
    try {
      const result = await headObject(token.trim(), trimmedKey);
      if (result) {
        setMetadata(result);
        setMessage({ kind: 'success', text: `${trimmedKey} exists.` });
      } else {
        setMessage({ kind: 'info', text: `No object is stored at "${trimmedKey}" yet.` });
      }
    } catch (err) {
      reportError(err, 'Could not check that key.');
    } finally {
      setBusy(null);
    }
  }

  async function handleUpload() {
    const trimmedKey = key.trim();
    if (!token.trim() || !trimmedKey || !file) return;
    setBusy('upload');
    setProgress(0);
    setMessage(null);
    setConfirmDelete(false);
    try {
      const manifest = await putObject(token.trim(), trimmedKey, file, setProgress);
      setMetadata({
        generation: String(manifest.generation),
        sha256: manifest.sha256,
        contentType: manifest.contentType,
        size: manifest.size,
      });
      setMessage({
        kind: 'success',
        text: `Uploaded ${trimmedKey} (${manifest.replicas.length} replica${
          manifest.replicas.length === 1 ? '' : 's'
        }).`,
      });
      setFile(null);
      if (fileInputRef.current) fileInputRef.current.value = '';
    } catch (err) {
      reportError(err, 'Upload failed.');
    } finally {
      setBusy(null);
    }
  }

  async function handleDownload() {
    const trimmedKey = key.trim();
    if (!token.trim() || !trimmedKey) return;
    setBusy('download');
    setMessage(null);
    setConfirmDelete(false);
    try {
      await downloadObject(token.trim(), trimmedKey);
      setMessage({ kind: 'success', text: `Downloading ${trimmedKey}…` });
    } catch (err) {
      reportError(err, 'Download failed.');
    } finally {
      setBusy(null);
    }
  }

  async function handleDelete() {
    const trimmedKey = key.trim();
    if (!token.trim() || !trimmedKey) return;
    if (!confirmDelete) {
      setConfirmDelete(true);
      return;
    }
    setBusy('delete');
    setMessage(null);
    try {
      await deleteObject(token.trim(), trimmedKey);
      setMessage({ kind: 'success', text: `Deleted ${trimmedKey}.` });
      setMetadata(null);
    } catch (err) {
      reportError(err, 'Delete failed.');
    } finally {
      setBusy(null);
      setConfirmDelete(false);
    }
  }

  const ready = token.trim().length > 0 && key.trim().length > 0;

  return (
    <div className="page">
      <div className="card">
        <div className="brand">
          <span className="brand__mark">V</span>
          <div>
            <div className="brand__name">Vault</div>
            <div className="brand__sub">Object storage</div>
          </div>
        </div>

        <label className="field-label" htmlFor="token">
          Operator token
        </label>
        <input
          id="token"
          type="password"
          autoComplete="off"
          spellCheck={false}
          className="input input--mono"
          placeholder="dev-vault-token"
          value={token}
          onChange={(e) => rememberToken(e.target.value)}
        />

        <label className="field-label" htmlFor="key">
          Object key
        </label>
        <input
          id="key"
          type="text"
          className="input input--mono"
          placeholder="e.g. logs/archive.tar.gz"
          value={key}
          onChange={(e) => {
            setKey(e.target.value);
            setConfirmDelete(false);
          }}
        />

        <label className="field-label" htmlFor="file">
          File to upload
        </label>
        <input
          id="file"
          ref={fileInputRef}
          type="file"
          className="input"
          onChange={onFileChange}
        />
        {file && (
          <div className="file-chip">
            <span className="mono">{file.name}</span>
            <span className="file-chip__size">{formatBytes(file.size)}</span>
          </div>
        )}

        {busy === 'upload' && (
          <div className="progress">
            <div className="progress__bar" style={{ width: `${progress}%` }} />
          </div>
        )}

        <div className="button-row">
          <button className="btn" disabled={!ready || busy !== null} onClick={handleCheck}>
            {busy === 'check' ? 'Checking…' : 'Check'}
          </button>
          <button
            className="btn btn--primary"
            disabled={!ready || !file || busy !== null}
            onClick={handleUpload}
          >
            {busy === 'upload' ? `Uploading… ${progress}%` : 'Upload'}
          </button>
          <button className="btn" disabled={!ready || busy !== null} onClick={handleDownload}>
            {busy === 'download' ? 'Downloading…' : 'Download'}
          </button>
          <button
            className={`btn btn--danger${confirmDelete ? ' btn--danger-confirm' : ''}`}
            disabled={!ready || busy !== null}
            onClick={handleDelete}
          >
            {busy === 'delete' ? 'Deleting…' : confirmDelete ? 'Confirm delete' : 'Delete'}
          </button>
        </div>

        {message && <div className={`banner banner--${message.kind}`}>{message.text}</div>}

        {metadata && (
          <div className="meta-grid">
            <div className="meta-field">
              <div className="meta-field__label">Generation</div>
              <div className="meta-field__value mono">{metadata.generation}</div>
            </div>
            <div className="meta-field">
              <div className="meta-field__label">Size</div>
              <div className="meta-field__value">{formatBytes(metadata.size)}</div>
            </div>
            <div className="meta-field">
              <div className="meta-field__label">Content type</div>
              <div className="meta-field__value">{metadata.contentType || '—'}</div>
            </div>
            <div className="meta-field">
              <div className="meta-field__label">SHA-256</div>
              <div className="meta-field__value mono">{shortHash(metadata.sha256)}</div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
