package ai.agentmesh.app;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

/**
 * What the WebView hands to another app, and what it must keep for itself.
 *
 * The narrowness is the point: an intent: URL can name any component, so
 * answering "yes" to more schemes than a payment needs would turn a page in
 * the WebView into a way to fire arbitrary intents.
 */
public class PaymentAppLauncherTest {

    @Test
    public void claimsUpiDeepLinks() {
        assertTrue(PaymentAppLauncher.handles("upi://pay?pa=merchant@bank&am=100"));
        assertTrue(PaymentAppLauncher.handles("UPI://pay?pa=merchant@bank"));
    }

    @Test
    public void claimsAndroidIntentUrls() {
        // What a hosted checkout emits to name one payment app, with an https
        // fallback for a device that does not have it.
        assertTrue(PaymentAppLauncher.handles(
                "intent://pay?pa=merchant@bank#Intent;scheme=upi;package=com.google.android.apps.nbu.paisa.user;end"));
        assertTrue(PaymentAppLauncher.handles("Intent://pay#Intent;scheme=upi;end"));
    }

    @Test
    public void leavesOrdinaryWebNavigationAlone() {
        // The hosted card page is plain https and belongs in the WebView.
        assertFalse(PaymentAppLauncher.handles("https://payments.cashfree.com/order/abc"));
        assertFalse(PaymentAppLauncher.handles("http://localhost:3000/billing"));
        assertFalse(PaymentAppLauncher.handles("https://www.agent-mesh.app/billing"));
    }

    @Test
    public void refusesSchemesAPaymentNeverUses() {
        // file: and content: reach the device's own storage; javascript: runs
        // in the page. None of them is a payment, so none is claimed.
        assertFalse(PaymentAppLauncher.handles("file:///data/data/ai.agentmesh.app/databases/x"));
        assertFalse(PaymentAppLauncher.handles("content://com.android.providers/1"));
        assertFalse(PaymentAppLauncher.handles("javascript:alert(1)"));
        assertFalse(PaymentAppLauncher.handles("tel:+911234567890"));
        assertFalse(PaymentAppLauncher.handles("market://details?id=com.example"));
    }

    @Test
    public void survivesNothingAtAll() {
        assertFalse(PaymentAppLauncher.handles(null));
        assertFalse(PaymentAppLauncher.handles(""));
    }
}
