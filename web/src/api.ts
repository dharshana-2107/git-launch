// Every call to the Go API goes through this file, so URLs, JSON handling
// and error handling live in one place instead of in every page.

export type Project = {
  id: number;
  name: string;
  repoUrl: string;
  branch: string;
  subdomain: string;
  envVars?: Record<string, string>;
  createdAt: string;
  updatedAt: string;
};

export type ProjectInput = {
  name: string;
  repoUrl: string;
  branch: string;
  subdomain: string;
  envVars: Record<string, string>;
};

// ApiError carries the API's error message plus per-field messages
// (from 422 validation and 409 conflict responses) so forms can show them.
export class ApiError extends Error {
  status: number;
  fields: Record<string, string>;

  constructor(status: number, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.status = status;
    this.fields = fields;
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
  });

  if (res.status === 204) return undefined as T; // DELETE returns no body

  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, body.error ?? `Request failed (${res.status})`, body.fields);
  }
  return body as T;
}

export const api = {
  listProjects: () => request<Project[]>("/api/projects"),
  getProject: (id: number) => request<Project>(`/api/projects/${id}`),
  createProject: (input: ProjectInput) =>
    request<Project>("/api/projects", { method: "POST", body: JSON.stringify(input) }),
  updateProject: (id: number, input: ProjectInput) =>
    request<Project>(`/api/projects/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteProject: (id: number) => request<void>(`/api/projects/${id}`, { method: "DELETE" }),
};

// The address each app will be served at (Traefik routes it from phase 3).
export const appUrl = (subdomain: string) => `http://${subdomain}.localhost`;
