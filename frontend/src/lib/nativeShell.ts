// The web build's stand-in for the native shell.
//
// `@native` resolves here in a browser build and to ../mobile/src in a native
// one (see next.config.ts). Having both sides resolve to a real module is what
// lets the calling code be written once, with no conditional imports and no
// bundler warning about a module that only sometimes exists.
//
// Every method is a no-op that answers honestly: there is no shell, so there
// is no session to persist and no geofence to register.
export interface NativeShell {
  onSignedIn(token: string): Promise<void>;
  onSignedOut(): Promise<void>;
  setGeofence(
    workflowId: string,
    fence: { lat: number; lng: number; radiusM: number },
  ): Promise<boolean>;
  clearGeofence(workflowId: string): Promise<void>;
}

export async function boot(): Promise<string | null> {
  return null;
}

export const shell: NativeShell = {
  async onSignedIn() {},
  async onSignedOut() {},
  // False, not true: a browser genuinely cannot register a geofence, and
  // reporting success would let the UI claim a trigger is armed when nothing
  // is watching.
  async setGeofence() {
    return false;
  },
  async clearGeofence() {},
};
