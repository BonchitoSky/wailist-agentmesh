import { describe, it, expect, vi, beforeEach } from "vitest";

// What persistNativeSession must guarantee: on a device that cannot keep the
// session, the sign-in does not quietly succeed.
//
// It used to. The shell write was fired off with `void ... .catch(console.error)`,
// so a Keystore failure left the UI signed in, the session working until the
// app was closed, and the next launch showing a sign-in screen with nothing
// anywhere explaining why.

const signOut = vi.fn(async () => {});
const setAuthToken = vi.fn();
const onSignedIn = vi.fn(async (token: string) => {
  void token;
});

vi.mock("@/lib/api", () => ({
  auth: { signOut: () => signOut() },
}));
vi.mock("@/lib/nativeAuth", () => ({
  IS_NATIVE: true,
  authReady: Promise.resolve(),
  setAuthToken: (t: string | null) => setAuthToken(t),
}));
vi.mock("@/native", () => ({
  shell: { onSignedIn: (t: string) => onSignedIn(t) },
}));

beforeEach(() => {
  signOut.mockClear();
  setAuthToken.mockClear();
  onSignedIn.mockClear();
});

describe("persistNativeSession", () => {
  it("attaches the token when the device keeps it", async () => {
    const { persistNativeSession } = await import("./useAuth");
    await expect(persistNativeSession("tok_live")).resolves.toBeUndefined();
    expect(onSignedIn).toHaveBeenCalledWith("tok_live");
    expect(setAuthToken).toHaveBeenCalledWith("tok_live");
    expect(signOut).not.toHaveBeenCalled();
  });

  it("throws, and leaves nothing signed in, when the store refuses", async () => {
    onSignedIn.mockRejectedValueOnce(new Error("keystore key invalidated"));
    const { persistNativeSession, SessionPersistError } =
      await import("./useAuth");

    await expect(persistNativeSession("tok_live")).rejects.toBeInstanceOf(
      SessionPersistError,
    );
    // Revoked server-side, then detached locally. Order matters: the request
    // needs the token still attached to be able to revoke it.
    expect(signOut).toHaveBeenCalledTimes(1);
    expect(setAuthToken).toHaveBeenLastCalledWith(null);
  });

  it("keeps the underlying failure as the cause", async () => {
    const underlying = new Error("keystore key invalidated");
    onSignedIn.mockRejectedValueOnce(underlying);
    const { persistNativeSession } = await import("./useAuth");

    // The message shown to the user is written for them, so the real reason
    // has to survive somewhere a log can reach it.
    await expect(persistNativeSession("tok_live")).rejects.toMatchObject({
      cause: underlying,
    });
  });

  it("still reports the store failure when the revoke also fails", async () => {
    onSignedIn.mockRejectedValueOnce(new Error("keystore key invalidated"));
    signOut.mockRejectedValueOnce(new Error("offline"));
    const { persistNativeSession, SessionPersistError } =
      await import("./useAuth");

    // A failed revoke must not mask the failure the caller is being told to
    // stop for.
    await expect(persistNativeSession("tok_live")).rejects.toBeInstanceOf(
      SessionPersistError,
    );
    expect(setAuthToken).toHaveBeenLastCalledWith(null);
  });
});
