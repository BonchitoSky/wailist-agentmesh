"use client";
import { useState } from "react";
import { auth, type AuthUser } from "@/lib/api";
import {
  FormStatus,
  ReadOnlyRow,
  SaveButton,
  SettingRow,
  SettingsSection,
  inputStyle,
} from "@/components/settings/ui";

type SaveState = "idle" | "saving" | "saved" | "error";

const memberSince = (iso?: string): string => {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? "—"
    : new Intl.DateTimeFormat("en", {
        year: "numeric",
        month: "long",
        day: "numeric",
        // Pinned, or the server (UTC) and a browser west of it disagree:
        // 2026-01-01T00:00:00Z is "January 1, 2026" in UTC and
        // "December 31, 2025" everywhere in the Americas, which hydrates
        // as a mismatch. A signup date is the same day for everyone.
        timeZone: "UTC",
      }).format(d);
};

// `user` is deliberately non-nullable: the form below seeds its state with
// useState, which ignores later prop changes, so mounting with a half-loaded
// user would strand blank inputs and let a save wipe the stored organisation.
// SettingsPage holds this back until /auth/me resolves — the type is what keeps
// that contract from being quietly dropped later.
export function AccountSection({
  user,
  onProfileSaved,
}: {
  user: AuthUser;
  onProfileSaved: (name: string, org: string) => Promise<void>;
}) {
  const [name, setName] = useState(user.name);
  const [org, setOrg] = useState(user.orgName);
  const [profileState, setProfileState] = useState<SaveState>("idle");
  const [profileMessage, setProfileMessage] = useState("");

  const saveProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setProfileState("error");
      setProfileMessage("Name can't be empty.");
      return;
    }
    setProfileState("saving");
    try {
      await onProfileSaved(name.trim(), org.trim());
      setProfileState("saved");
      setProfileMessage("Profile updated.");
    } catch (err) {
      setProfileState("error");
      setProfileMessage(
        err instanceof Error ? err.message : "Could not save your profile.",
      );
    }
  };

  return (
    <>
      <SettingsSection
        id="account"
        title="Account"
        description="Your name and organisation appear in the top bar and on anything you share."
      >
        <form onSubmit={saveProfile} style={{ display: "grid", gap: 18 }}>
          <SettingRow label="Display name" htmlFor="set-name">
            <input
              id="set-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={64}
              placeholder="Ada Lovelace"
              style={inputStyle}
            />
          </SettingRow>
          <SettingRow
            label="Organisation"
            htmlFor="set-org"
            hint="Shown next to the logo. Leave blank for a personal workspace."
          >
            <input
              id="set-org"
              value={org}
              onChange={(e) => setOrg(e.target.value)}
              maxLength={64}
              placeholder="Acme Capital"
              style={inputStyle}
            />
          </SettingRow>
          <ReadOnlyRow label="Email" value={user.email} mono />
          {/* createdAt stays optional on AuthUser — a response cached from
              before the field existed won't carry it — so memberSince still
              handles undefined. */}
          <ReadOnlyRow
            label="Member since"
            value={memberSince(user.createdAt)}
          />
          <SaveButton saving={profileState === "saving"} />
          <FormStatus state={profileState} message={profileMessage} />
        </form>
      </SettingsSection>

      <PasswordSection />
    </>
  );
}

// Split out so a failed password change never clears the profile form's state,
// and so the password rules get their own explanation rather than being
// crammed into the account panel.
function PasswordSection() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [state, setState] = useState<SaveState>("idle");
  const [message, setMessage] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (next.length < 8) {
      setState("error");
      setMessage("New password must be at least 8 characters.");
      return;
    }
    // Caught here rather than server-side: the backend has no reason to know
    // about a confirmation field, and a round-trip to say "they don't match"
    // is a worse experience than saying it immediately.
    if (next !== confirm) {
      setState("error");
      setMessage("The new passwords don't match.");
      return;
    }
    setState("saving");
    try {
      await auth.changePassword(current, next);
      setState("saved");
      setMessage("Password changed.");
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      // Carries the server's own wording, which distinguishes a wrong current
      // password from an OAuth-only account that has no password at all.
      setState("error");
      setMessage(
        err instanceof Error ? err.message : "Could not change your password.",
      );
    }
  };

  return (
    <SettingsSection
      id="password"
      title="Password"
      description="Changing your password does not sign you out on other devices — sessions can't be revoked yet."
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 18 }}>
        <SettingRow label="Current password" htmlFor="set-pw-current">
          <input
            id="set-pw-current"
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            style={inputStyle}
          />
        </SettingRow>
        <SettingRow
          label="New password"
          htmlFor="set-pw-new"
          hint="At least 8 characters."
        >
          <input
            id="set-pw-new"
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            style={inputStyle}
          />
        </SettingRow>
        <SettingRow label="Confirm new password" htmlFor="set-pw-confirm">
          <input
            id="set-pw-confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            style={inputStyle}
          />
        </SettingRow>
        <SaveButton saving={state === "saving"} disabled={!current || !next}>
          Change password
        </SaveButton>
        <FormStatus state={state} message={message} />
      </form>
    </SettingsSection>
  );
}
