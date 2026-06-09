// Lightweight auth cache to avoid duplicate api.auth.me() calls.
// AuthGuard writes the user on mount; Sidebar reads it without
// making another network request.

import type { AuthUser } from "./api";

let cachedUser: AuthUser | null = null;
let fetchPromise: Promise<AuthUser | null> | null = null;

export function getCachedUser(): AuthUser | null {
  return cachedUser;
}

export function setCachedUser(user: AuthUser | null): void {
  cachedUser = user;
}

/**
 * Returns a shared promise that resolves to the current user.
 * The first caller triggers the actual API request; subsequent
 * callers reuse the same in-flight promise.
 */
export function fetchUser(): Promise<AuthUser | null> {
  if (cachedUser) return Promise.resolve(cachedUser);
  if (!fetchPromise) {
    // Lazy import to avoid circular deps
    fetchPromise = import("./api").then(({ api }) =>
      api.auth.me().then((r) => {
        cachedUser = r.user;
        fetchPromise = null;
        return r.user;
      }).catch(() => {
        fetchPromise = null;
        return null;
      })
    );
  }
  return fetchPromise;
}
