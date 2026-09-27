import { SharePreview } from "@/components/share/SharePreview";
import { MOBILE_SHELL_ID } from "@/lib/routes";

// The public end of a share link.
//
// /s sits OUTSIDE the PROTECTED prefixes in middleware.ts, deliberately and
// not by accident: every other workflow route bounces a signed-out visitor to
// /signin, which would make a link posted anywhere public show a stranger
// nothing but a login wall. Adding /s to that list would silently break the
// feature, so if you are here to do that, read SharePreview first.
//
// The native shell ships a static export, which cannot prerender a page per
// token -- the tokens belong to links that do not exist at build time. Unlike
// /workflows/[id], though, this shell page is not a working screen: nothing in
// the app opens a share link inside the WebView, and lib/readonly.ts blocks
// importing on native regardless. It exists only because `output: export`
// refuses to build a dynamic route whose generateStaticParams returns nothing
// ("at least one route must be generated"), which is a build failure the
// android-pr workflow would have caught after this had already merged.
//
// The web build returns no params and keeps rendering every token on demand,
// exactly as /workflows/[id] does.
export function generateStaticParams() {
  return process.env.MOBILE_BUILD === "1" ? [{ token: MOBILE_SHELL_ID }] : [];
}

export default async function SharePageRoute({
  params,
}: {
  params: Promise<{ token: string }>;
}) {
  const { token } = await params;
  // No Suspense boundary here, unlike /workflows/[id]: that one wraps a tree
  // that calls useSearchParams(), which Next 16 refuses to build without one.
  // SharePreview reads nothing from the query string, so a boundary would be
  // ceremony. Add one the moment it does.
  return <SharePreview token={token} />;
}
