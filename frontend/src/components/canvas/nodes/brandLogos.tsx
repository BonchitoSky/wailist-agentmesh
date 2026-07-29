import * as simpleIcons from "simple-icons";
import type { ReactNode } from "react";

// Maps a node template id to its simple-icons export name. The logo is drawn in
// currentColor (monochrome) so it inherits the exact colour the placeholder
// letter used — the node's icon box, size, and styling are untouched; only the
// glyph shape changes from a letter to the service's real mark.
//
// Lookup is dynamic (simpleIcons[name]) so any brand simple-icons doesn't ship
// resolves to `undefined` and cleanly falls back to the original letter — no
// build break, no missing-icon gaps.
const BRAND_ICON_NAMES: Record<string, string> = {
  // LLM providers
  openai: "siOpenai",
  anthropic: "siAnthropic",
  gemini: "siGooglegemini",
  mistral: "siMistralai",
  groq: "siGroq",
  // Messaging
  slack: "siSlack",
  discord: "siDiscord",
  teams: "siMicrosoftteams",
  google_chat: "siGooglechat",
  ntfy: "siNtfy",
  telegram: "siTelegram",
  // Dev tools
  github: "siGithub",
  gitlab: "siGitlab",
  jira: "siJira",
  linear: "siLinear",
  sentry: "siSentry",
  // Productivity
  notion: "siNotion",
  airtable: "siAirtable",
  trello: "siTrello",
  asana: "siAsana",
  clickup: "siClickup",
  todoist: "siTodoist",
  // Data / commerce
  hubspot: "siHubspot",
  mailchimp: "siMailchimp",
  supabase: "siSupabase",
  woocommerce: "siWoocommerce",
  // Media
  elevenlabs: "siElevenlabs",
};

type SimpleIcon = { path: string; title: string };

function iconFor(template?: string): SimpleIcon | undefined {
  if (!template) return undefined;
  const name = BRAND_ICON_NAMES[template];
  if (!name) return undefined;
  const icon = (simpleIcons as Record<string, SimpleIcon | undefined>)[name];
  return icon?.path ? icon : undefined;
}

// Custom marks for brands simple-icons no longer ships (Slack and Microsoft
// Teams were both removed at the brands' request). Drawn in currentColor on the
// same 24-unit grid so they match every other logo and the node's icon box.
const CUSTOM_LOGOS: Record<
  string,
  { title: string; render: (size: number) => ReactNode }
> = {
  slack: {
    title: "Slack",
    render: (size) => (
      <svg
        role="img"
        aria-label="Slack"
        viewBox="0 0 24 24"
        width={size}
        height={size}
        fill="currentColor"
        style={{ display: "block" }}
      >
        {[0, 90, 180, 270].map((a) => (
          <g key={a} transform={`rotate(${a} 12 12)`}>
            <rect x="12" y="9.3" width="6.4" height="2.7" rx="1.35" />
            <rect x="15.7" y="12" width="2.7" height="2.7" rx="1.35" />
          </g>
        ))}
      </svg>
    ),
  },
  teams: {
    title: "Microsoft Teams",
    render: (size) => (
      <svg
        role="img"
        aria-label="Microsoft Teams"
        viewBox="0 0 24 24"
        width={size}
        height={size}
        fill="currentColor"
        style={{ display: "block" }}
      >
        <circle cx="8" cy="5.6" r="2.3" />
        <path
          fillRule="evenodd"
          clipRule="evenodd"
          d="M10 7 h8 a2 2 0 0 1 2 2 v8 a2 2 0 0 1 -2 2 h-8 a2 2 0 0 1 -2 -2 v-8 a2 2 0 0 1 2 -2 Z M11 10 v1.7 h2.1 v5.3 h1.8 v-5.3 h2.1 v-1.7 Z"
        />
      </svg>
    ),
  },
};

// Renders the service's real brand mark in place of the placeholder letter when
// one exists for the node's template; otherwise renders the letter unchanged.
export function BrandLogo({
  template,
  fallback,
  size = 15,
}: {
  template?: string;
  fallback: ReactNode;
  size?: number;
}) {
  const custom = template ? CUSTOM_LOGOS[template] : undefined;
  if (custom) return <>{custom.render(size)}</>;

  const icon = iconFor(template);
  if (!icon) return <>{fallback}</>;
  return (
    <svg
      role="img"
      aria-label={icon.title}
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="currentColor"
      style={{ display: "block" }}
    >
      <path d={icon.path} />
    </svg>
  );
}
