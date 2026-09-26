export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function authHeaders(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

// The server treats everything after "objects/" as the literal key, slashes
// included, so each path segment is encoded individually rather than the
// whole key at once.
function encodeKey(key: string): string {
  return key
    .split('/')
    .map((segment) => encodeURIComponent(segment))
    .join('/');
}

async function readErrorText(res: Response): Promise<string> {
  const text = (await res.text()).trim();
  return text || res.statusText || `Request failed (${res.status})`;
}

export interface ObjectHead {
  generation: string;
  sha256: string;
  contentType: string;
  size: number;
}

export async function headObject(token: string, key: string): Promise<ObjectHead | null> {
  const res = await fetch(`/api/objects/${encodeKey(key)}`, {
    method: 'HEAD',
    headers: authHeaders(token),
  });
  if (res.status === 404) return null;
  if (!res.ok) throw new ApiError(res.status, await readErrorText(res));
  return {
    generation: (res.headers.get('ETag') || '').replace(/"/g, ''),
    sha256: res.headers.get('X-Content-SHA256') || '',
    contentType: res.headers.get('Content-Type') || 'application/octet-stream',
    size: Number(res.headers.get('Content-Length') || 0),
  };
}

export async function downloadObject(token: string, key: string): Promise<void> {
  const res = await fetch(`/api/objects/${encodeKey(key)}`, { headers: authHeaders(token) });
  if (!res.ok) throw new ApiError(res.status, await readErrorText(res));
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = key.split('/').pop() || key;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export async function deleteObject(token: string, key: string): Promise<void> {
  const res = await fetch(`/api/objects/${encodeKey(key)}`, {
    method: 'DELETE',
    headers: authHeaders(token),
  });
  if (res.status === 404) throw new ApiError(404, 'No object exists at this key.');
  if (!res.ok) throw new ApiError(res.status, await readErrorText(res));
}

export interface Manifest {
  key: string;
  generation: number;
  size: number;
  sha256: string;
  contentType: string;
  replicas: string[];
}

export function putObject(
  token: string,
  key: string,
  file: File,
  onProgress?: (percent: number) => void
): Promise<Manifest> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', `/api/objects/${encodeKey(key)}`);
    xhr.setRequestHeader('Authorization', `Bearer ${token}`);
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText));
        } catch {
          reject(new ApiError(xhr.status, 'The server returned a malformed response.'));
        }
      } else {
        reject(new ApiError(xhr.status, xhr.responseText.trim() || `Request failed (${xhr.status})`));
      }
    };
    xhr.onerror = () => reject(new ApiError(0, 'Network error while uploading.'));
    xhr.send(file);
  });
}
