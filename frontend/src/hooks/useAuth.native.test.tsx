import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";

// On the phone the session is a bearer token the shell keeps on disk. When the
// server answers that the token is no good, it has to go -- from memory and
// from the device -- or NativeBoot still counts it as a session and a tapped
// notification opens a protected screen without signing in.
const state = vi.hoisted(() => ({
  me: vi.fn(),
  setAuthToken: vi.fn(),
  onSignedOut: vi.fn(async () => {}),
}));

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, auth: { ...actual.auth, me: state.me } };
});
vi.mock("@/lib/nativeAuth", () => ({
  IS_NATIVE: true,
  authReady: Promise.resolve(),
  setAuthToken: state.setAuthToken,
}));
vi.mock("@/native", () => ({ shell: { onSignedOut: state.onSignedOut } }));

import { AuthCheckError } from "@/lib/api";
import { useAuth } from "./useAuth";

afterEach(() => vi.clearAllMocks());

describe("useAuth on the phone", () => {
  it.each([401, 403])(
    "drops a token the server rejects with %i, in memory and on the device",
    async (status) => {
      state.me.mockRejectedValue(new AuthCheckError(status));
      const { result } = renderHook(() => useAuth());

      await waitFor(() => expect(result.current.loading).toBe(false));
      expect(result.current.signedIn).toBe(false);
      expect(state.setAuthToken).toHaveBeenCalledWith(null);
      await waitFor(() => expect(state.onSignedOut).toHaveBeenCalledTimes(1));
    },
  );

  // A server failure says nothing about the token, so it stays.
  it("keeps the token when the server fails", async () => {
    state.me.mockRejectedValue(new AuthCheckError(503));
    const { result } = renderHook(() => useAuth());

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.offline).toBe(true);
    expect(state.setAuthToken).not.toHaveBeenCalled();
    expect(state.onSignedOut).not.toHaveBeenCalled();
  });
});
