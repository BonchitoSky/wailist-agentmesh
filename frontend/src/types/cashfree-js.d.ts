declare module "@cashfreepayments/cashfree-js" {
  interface CashfreeCheckoutOptions {
    paymentSessionId: string;
    redirectTarget?: "_modal" | "_self" | "_blank";
    returnUrl?: string;
  }

  interface CashfreeCheckoutResult {
    error?: { message?: string; type?: string };
    redirect?: boolean;
    paymentDetails?: Record<string, unknown>;
  }

  interface CashfreeInstance {
    checkout(options: CashfreeCheckoutOptions): Promise<CashfreeCheckoutResult>;
  }

  interface LoadOptions {
    mode: "production" | "sandbox";
  }

  export function load(options: LoadOptions): Promise<CashfreeInstance>;
}
