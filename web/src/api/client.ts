/**
 * REST API client for mtix.
 * All requests include the X-Requested-With: mtix header per security requirements.
 */

const BASE_URL = "/api/v1";

/** Standard request options. */
interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

/** API error with status code and message. */
export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/**
 * Make an authenticated API request.
 * Includes CSRF header per security requirements.
 */
async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, signal } = options;

  const headers: Record<string, string> = {
    "X-Requested-With": "mtix",
  };

  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }

  const response = await fetch(`${BASE_URL}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });

  if (!response.ok) {
    const errorBody = await response.json().catch(() => ({}));
    const message =
      (errorBody as { error?: string }).error ??
      `Request failed with status ${response.status}`;
    throw new ApiError(response.status, message);
  }

  // Handle 204 No Content.
  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}

/** GET request. */
export function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  return request<T>(path, { signal });
}

/** POST request. */
export function post<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  return request<T>(path, { method: "POST", body, signal });
}

/** PUT request. */
export function put<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  return request<T>(path, { method: "PUT", body, signal });
}

/** PATCH request. */
export function patch<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  return request<T>(path, { method: "PATCH", body, signal });
}

/** DELETE request. */
export function del<T>(path: string, signal?: AbortSignal): Promise<T> {
  return request<T>(path, { method: "DELETE", signal });
}
