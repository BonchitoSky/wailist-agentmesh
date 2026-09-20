package ai.agentmesh.app;

import android.content.ActivityNotFoundException;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.util.Log;

import java.net.URISyntaxException;

/**
 * Hands a payment app a URL the WebView cannot open itself.
 *
 * A hosted card page is ordinary https and loads in the WebView. UPI is not:
 * choosing Google Pay or PhonePe navigates to an {@code upi://} or
 * {@code intent://} URL whose whole purpose is to leave the browser and open
 * another app. A WebView with no {@code shouldOverrideUrlLoading} does not know
 * that, so it tries to load the URL as a page, fails, and the payment simply
 * stops with nothing on screen to explain it -- which in India removes the
 * payment method most people would reach for first.
 *
 * Deliberately narrow. It answers only for schemes a payment flow uses, so a
 * compromised page cannot use it as a way to fire arbitrary intents: an
 * {@code intent://} URL can name any component, and handing those to
 * startActivity without restriction is a known Android hazard. Everything else
 * is left to the WebView.
 */
final class PaymentAppLauncher {
    private static final String TAG = "PaymentAppLauncher";

    private PaymentAppLauncher() {}

    /** Whether {@code url} is one this class will try to hand to another app. */
    static boolean handles(String url) {
        if (url == null) return false;
        String u = url.toLowerCase();
        // upi: is the UPI deep link. intent: is Android's own wrapper, which
        // Cashfree uses to name a specific payment app with an https fallback.
        return u.startsWith("upi:") || u.startsWith("intent:");
    }

    /**
     * Opens {@code url} in whichever app claims it.
     *
     * @return true when the URL was consumed, so the WebView must not also try
     *         to load it. False means nothing could handle it and the caller
     *         should carry on as normal.
     */
    static boolean open(Context context, String url) {
        if (!handles(url)) return false;
        try {
            Intent intent = url.toLowerCase().startsWith("intent:")
                    ? Intent.parseUri(url, Intent.URI_INTENT_SCHEME)
                    : new Intent(Intent.ACTION_VIEW, Uri.parse(url));

            // An intent: URL carries whatever component its author wrote. The
            // selector is dropped and the component cleared so this resolves
            // like a plain view of the data -- any installed app may claim it,
            // but the page cannot name one of ours and reach a component that
            // was never meant to be launched from a web page.
            intent.setComponent(null);
            intent.setSelector(null);
            intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);

            context.startActivity(intent);
            return true;
        } catch (URISyntaxException e) {
            Log.w(TAG, "payment URL was not a valid intent: " + e.getMessage());
            return false;
        } catch (ActivityNotFoundException e) {
            // No UPI app installed. Returning false lets the WebView fall back
            // to the https alternative an intent: URL usually carries, which
            // is the hosted page telling the payer to pick another method.
            Log.w(TAG, "no app installed for this payment URL");
            return false;
        } catch (SecurityException e) {
            Log.w(TAG, "refused to launch payment URL: " + e.getMessage());
            return false;
        }
    }
}
