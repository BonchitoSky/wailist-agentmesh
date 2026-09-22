"use client";
import { useRouter } from "next/navigation";
import { useCredits } from "@/lib/credits/store";
import { formatDollars } from "@/lib/workflowMeta";

const usd = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
});

// The top of the phone Workflows screen: the title with a way to top up, then
// what the workspace holds on the left and what is left to spend on the right.
// The page reads the balance on mount and on pull-to-refresh; this only shows
// it.
export function WorkflowsPhoneHeader({
  total,
  shown,
  spend,
}: {
  total: number;
  // How many survive the search and filter. Saying "1 of 6" keeps the count
  // from contradicting the list underneath it.
  shown: number;
  // Dollars spent across every workflow, over the same 30 days the list counts.
  spend: number;
}) {
  const router = useRouter();
  const { balanceUSD, balanceKnown } = useCredits();
  const narrowed = shown !== total;
  const count = narrowed ? `${shown} of ${total}` : `${total} total`;
  // Spoken in place of the visible text, so it has to say the same thing.
  const spokenCount = narrowed
    ? `${shown} of ${total} workflows shown`
    : `${total} workflows`;
  const spent = formatDollars(spend);
  return (
    <header className="wfp-head">
      <div className="wfp-head__top">
        <h1 className="wfp-title">Workflows</h1>
      </div>
      <div className="wfp-head__facts">
        <span
          className="wfp-head__summary"
          aria-label={`${spokenCount}, ${spent} spent in the last 30 days`}
        >
          {count} <span aria-hidden>·</span> {spent} spent
        </span>
        {/* Two dollar figures on one line: this one says which it is. It is
            also the way to top up: the balance is what adding credits
            changes, so tapping it opens Credits. A separate "Add credits"
            button beside the title looked out of place on a screen this
            quiet. The chevron says it can be tapped; the label says where
            it goes. */}
        <button
          type="button"
          className="wfp-head__balance"
          aria-label={
            balanceKnown
              ? `Credit balance ${usd.format(balanceUSD)}, add credits`
              : "Credit balance not loaded yet, add credits"
          }
          onClick={() => router.push("/billing")}
        >
          <span className="wfp-head__balance-label" aria-hidden>
            Credit
          </span>{" "}
          {balanceKnown ? usd.format(balanceUSD) : "—"}
          <span className="wfp-head__balance-chevron" aria-hidden>
            ›
          </span>
        </button>
      </div>
    </header>
  );
}
