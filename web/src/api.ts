export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export async function api<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const { json, ...rest } = init
  const res = await fetch(path, {
    ...rest,
    credentials: 'same-origin',
    headers: json !== undefined ? { 'Content-Type': 'application/json', ...Object.fromEntries(new Headers(rest.headers)) } : rest.headers,
    body: json !== undefined ? JSON.stringify(json) : rest.body,
  })
  if (res.status === 401) {
    window.location.href = '/auth/login'
    throw new ApiError(401, 'login required')
  }
  const text = await res.text()
  let body: { error?: string } | undefined
  try {
    body = text ? JSON.parse(text) : undefined
  } catch {
    if (!res.ok) throw new ApiError(res.status, res.statusText || `HTTP ${res.status}`)
    throw new ApiError(res.status, 'invalid JSON response')
  }
  if (!res.ok) throw new ApiError(res.status, body?.error ?? res.statusText)
  return body as T
}

// upload posts a multipart form. The header proves the request came from this app (the server refuses forms without it).
export function upload<T>(path: string, form: FormData): Promise<T> {
  return api<T>(path, { method: 'POST', body: form, headers: { 'X-Crucible-Upload': '1' } })
}
