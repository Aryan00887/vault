import { useEffect, useRef, useState } from 'react';
import type { ChangeEvent, DragEvent } from 'react';
import { ApiError, deleteObject, downloadObject, headObject, putObject } from '../api';
import type { ObjectHead } from '../api';
import type { RecentObject } from '../types';
import { formatBytes, formatRelativeTime, shortHash } from '../utils/format';

const RECENT_KEY = 'vault:recent-objects';
const RECENT_LIMIT = 25;

function loadRecent(): RecentObject[] {
  try {
    const raw = window.localStorage.getItem(RECENT_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function saveRecent(list: RecentObject[]) {
  try {
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(list.slice(0, RECENT_LIMIT)));
  } catch {
    // storage unavailable; recent-objects memory is a convenience, not critical
  }
}

type WriteMode = 'overwrite' | 'create-only' | 'if-match';

export default function Objects({
  token,
  pushToast,
  onChanged,
  onUnauthorized,
}: {
  token: string;
  pushToast: (kind: 'success' | 'error', message: string) => void;
  onChanged: () => void;
  onUnauthorized: () => void;
}) {
  const [key, setKey] = useState('');
  const [lookupResult, setLookupResult] = useState<ObjectHead | 'not-found' | null>(null);
  const [looking, setLooking] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const [mode, setMode] = useState<WriteMode>('overwrite');
  const [ifMatchGen, setIfMatchGen] = useState('');
  const [uploading, setUploading] = useState(false);
  const [progress, setProgress] = useState(0);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [recent, setRecent] = useState<RecentObject[]>(() => loadRecent());
  const fileInputRef = useRef<HTMLInputElement>(null);
  const head = typeof lookupResult === 'object' && lookupResult !== null ? lookupResult : null;
  const exists = head !== null;

  useEffect(() => {
    setLookupResult(null);
    setConfirmingDelete(false);
  }, [key]);

  function upsertRecent(entry: RecentObject) {
    setRecent((current) => {
      const next = [entry, ...current.filter((r) => r.key !== entry.key)].slice(0, RECENT_LIMIT);
      saveRecent(next);
      return next;
    });
  }

  function handleUnauthorized(err: unknown): boolean {
    if (err instanceof ApiError && err.status === 401) {
      onUnauthorized();
      return true;
    }
    return false;
  }

  async function handleLookup() {
    const trimmed = key.trim();
    if (!trimmed) return;
    setLooking(true);
    try {
      const result = await headObject(token, trimmed);
      setLookupResult(result ?? 'not-found');
      if (result) {
        upsertRecent({
          key: trimmed,
          generation: result.generation,
          size: result.size,
          sha256: result.sha256,
          contentType: result.contentType,
          lastAction: 'looked up',
          at: Date.now(),
        });
      }
    } catch (err) {
      if (handleUnauthorized(err)) return;
      pushToast('error', err instanceof Error ? err.message : 'Could not look up that key.');
    } finally {
      setLooking(false);
    }
  }

  function pickFile(f: File | null) {
    setFile(f);
    if (f && !key.trim()) setKey(f.name);
  }

  function onFileInputChange(e: ChangeEvent<HTMLInputElement>) {
    pickFile(e.target.files?.[0] ?? null);
  }

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    pickFile(e.dataTransfer.files?.[0] ?? null);
  }

  async function handleUpload() {
    const trimmed = key.trim();
    if (!trimmed || !file) return;
    setUploading(true);
    setProgress(0);
    try {
      const manifest = await putObject(token, trimmed, file, {
        contentType: file.type || 'application/octet-stream',
        ifNoneMatch: mode === 'create-only',
        ifMatch: mode === 'if-match' && ifMatchGen.trim() ? ifMatchGen.trim() : undefined,
        onProgress: setProgress,
      });
      pushToast(
        'success',
        `Stored ${trimmed} — generation ${manifest.generation}, ${manifest.replicas.length} replica${
          manifest.replicas.length === 1 ? '' : 's'
        }.`
      );
      upsertRecent({
        key: trimmed,
        generation: String(manifest.generation),
        size: manifest.size,
        sha256: manifest.sha256,
        contentType: manifest.contentType,
        lastAction: 'stored',
        at: Date.now(),
      });
      setLookupResult({
        generation: String(manifest.generation),
        sha256: manifest.sha256,
        contentType: manifest.contentType,
        size: manifest.size,
      });
      setFile(null);
      if (fileInputRef.current) fileInputRef.current.value = '';
      onChanged();
    } catch (err) {
      if (handleUnauthorized(err)) return;
      pushToast('error', err instanceof Error ? err.message : 'Upload failed.');
    } finally {
      setUploading(false);
    }
  }

  async function handleDownload() {
    const trimmed = key.trim();
    if (!trimmed) return;
    try {
      await downloadObject(token, trimmed);
    } catch (err) {
      if (handleUnauthorized(err)) return;
      pushToast('error', err instanceof Error ? err.message : 'Download failed.');
    }
  }

  async function handleDelete() {
    const trimmed = key.trim();
    if (!trimmed) return;
    setDeleting(true);
    try {
      await deleteObject(token, trimmed);
      pushToast('success', `Deleted ${trimmed}.`);
      upsertRecent({
        key: trimmed,
        generation: head ? head.generation : '',
        size: 0,
        sha256: '',
        contentType: '',
        lastAction: 'deleted',
        at: Date.now(),
      });
      setLookupResult('not-found');
      setConfirmingDelete(false);
      onChanged();
    } catch (err) {
      if (handleUnauthorized(err)) return;
      pushToast('error', err instanceof Error ? err.message : 'Delete failed.');
    } finally {
      setDeleting(false);
    }
  }

  function copyHash(hash: string) {
    if (!hash) return;
    navigator.clipboard
      .writeText(hash)
      .then(() => pushToast('success', 'Checksum copied.'))
      .catch(() => pushToast('error', 'Could not copy to clipboard.'));
  }

  function clearRecent() {
    setRecent([]);
    saveRecent([]);
  }

  return (
    <div className="stack">
      <section className="panel">
        <div className="panel__header">
          <h2 className="panel__title">Look up an object</h2>
        </div>
        <div className="key-row">
          <input
            className="text-input text-input--mono"
            placeholder="e.g. logs/archive.tar.gz"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && handleLookup()}
          />
          <button className="btn btn--secondary" onClick={handleLookup} disabled={!key.trim() || looking}>
            {looking ? 'Looking up…' : 'Look up'}
          </button>
        </div>

        {lookupResult === 'not-found' && (
          <p className="panel__lead panel__lead--muted">No object is stored at this key yet.</p>
        )}

        {head && (
          <div className="metadata-grid">
            <MetaField label="Generation" value={head.generation} mono />
            <MetaField label="Size" value={formatBytes(head.size)} />
            <MetaField label="Content type" value={head.contentType || '—'} />
            <MetaField
              label="SHA-256"
              value={shortHash(head.sha256)}
              mono
              onClick={() => copyHash(head.sha256)}
              title="Click to copy full checksum"
            />
          </div>
        )}

        {exists && (
          <div className="button-row">
            <button className="btn btn--secondary btn--small" onClick={handleDownload}>
              Download
            </button>
            {!confirmingDelete ? (
              <button className="btn btn--danger btn--small" onClick={() => setConfirmingDelete(true)}>
                Delete object
              </button>
            ) : (
              <span className="confirm-inline">
                Delete {key.trim()}?
                <button className="btn btn--danger btn--small" onClick={handleDelete} disabled={deleting}>
                  {deleting ? 'Deleting…' : 'Confirm'}
                </button>
                <button className="btn btn--ghost btn--small" onClick={() => setConfirmingDelete(false)}>
                  Cancel
                </button>
              </span>
            )}
          </div>
        )}
      </section>

      <section className="panel">
        <div className="panel__header">
          <h2 className="panel__title">Store an object</h2>
        </div>
        <div
          className={`dropzone${dragOver ? ' dropzone--active' : ''}`}
          onDragOver={(e) => {
            e.preventDefault();
            setDragOver(true);
          }}
          onDragLeave={() => setDragOver(false)}
          onDrop={onDrop}
        >
          <input
            ref={fileInputRef}
            type="file"
            className="dropzone__input"
            onChange={onFileInputChange}
          />
          {file ? (
            <div className="dropzone__file">
              <span className="mono">{file.name}</span>
              <span className="dropzone__file-size">{formatBytes(file.size)}</span>
            </div>
          ) : (
            <div className="dropzone__prompt">
              Drop a file here, or click to choose one
            </div>
          )}
        </div>

        <div className="write-modes">
          <label className="radio">
            <input
              type="radio"
              checked={mode === 'overwrite'}
              onChange={() => setMode('overwrite')}
            />
            Create or overwrite
          </label>
          <label className="radio">
            <input
              type="radio"
              checked={mode === 'create-only'}
              onChange={() => setMode('create-only')}
            />
            Only if it doesn't exist yet
          </label>
          <label className="radio">
            <input
              type="radio"
              checked={mode === 'if-match'}
              onChange={() => setMode('if-match')}
            />
            Only if unchanged since generation
          </label>
          {mode === 'if-match' && (
            <input
              className="text-input text-input--mono text-input--narrow"
              placeholder="e.g. 3"
              value={ifMatchGen}
              onChange={(e) => setIfMatchGen(e.target.value)}
            />
          )}
        </div>

        {uploading && (
          <div className="progress">
            <div className="progress__bar" style={{ width: `${progress}%` }} />
          </div>
        )}

        <button
          className="btn btn--primary"
          onClick={handleUpload}
          disabled={!key.trim() || !file || uploading}
        >
          {uploading ? `Uploading… ${progress}%` : 'Store object'}
        </button>
      </section>

      <section className="panel">
        <div className="panel__header">
          <h2 className="panel__title">Recent keys</h2>
          {recent.length > 0 && (
            <button className="link-btn" onClick={clearRecent}>
              Clear
            </button>
          )}
        </div>
        <p className="panel__lead panel__lead--muted">
          Vault has no endpoint to list every object, so this is a local memory of keys you've used
          in this browser.
        </p>
        {recent.length === 0 ? (
          <p className="empty-state">Objects you store or look up will show up here.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Key</th>
                <th>Generation</th>
                <th>Size</th>
                <th>Last action</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((r) => (
                <tr key={r.key} className="table__row--clickable" onClick={() => setKey(r.key)}>
                  <td className="mono">{r.key}</td>
                  <td className="mono">{r.generation || '—'}</td>
                  <td className="num mono">{r.size ? formatBytes(r.size) : '—'}</td>
                  <td>{r.lastAction}</td>
                  <td className="num">{formatRelativeTime(r.at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

function MetaField({
  label,
  value,
  mono,
  onClick,
  title,
}: {
  label: string;
  value: string;
  mono?: boolean;
  onClick?: () => void;
  title?: string;
}) {
  return (
    <div className={`meta-field${onClick ? ' meta-field--clickable' : ''}`} onClick={onClick} title={title}>
      <div className="meta-field__label">{label}</div>
      <div className={`meta-field__value${mono ? ' mono' : ''}`}>{value}</div>
    </div>
  );
}
