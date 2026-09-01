package ai.agentmesh.app;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/**
 * Persists every currently-armed geofence's parameters so BootReceiver can
 * re-register all of them after a reboot, when there is no WebView alive to
 * ask GeofencePlugin's caller for them.
 *
 * Keyed by workflow id rather than one slot: GeofencingClient.addGeofences()
 * is additive, not a replace, so nothing stops a caller from arming a second
 * workflow's fence before disarming the first. A single-slot store would
 * silently drop the first fence's reboot recovery the moment that happened.
 */
final class GeofenceStore {

    private static final String PREFS = "AgentMeshGeofenceStore";
    private static final String KEY_FENCES = "fences";

    private GeofenceStore() {
    }

    static void save(Context context, String id, double lat, double lng, double radiusM) {
        SharedPreferences p = prefs(context);
        JSONObject fences = readAll(p);
        try {
            JSONObject fence = new JSONObject();
            fence.put("lat", lat);
            fence.put("lng", lng);
            fence.put("radiusM", radiusM);
            fences.put(id, fence);
        } catch (JSONException e) {
            return;
        }
        p.edit().putString(KEY_FENCES, fences.toString()).apply();
    }

    // Removes exactly this workflow's fence. Any other workflow's armed fence
    // is untouched, unlike a single clear() that would wipe them all.
    static void remove(Context context, String id) {
        SharedPreferences p = prefs(context);
        JSONObject fences = readAll(p);
        fences.remove(id);
        p.edit().putString(KEY_FENCES, fences.toString()).apply();
    }

    static List<Active> loadAll(Context context) {
        JSONObject fences = readAll(prefs(context));
        List<Active> out = new ArrayList<>();
        JSONArray ids = fences.names();
        if (ids == null) {
            return out;
        }
        for (int i = 0; i < ids.length(); i++) {
            try {
                String id = ids.getString(i);
                JSONObject fence = fences.getJSONObject(id);
                out.add(new Active(id, fence.getDouble("lat"), fence.getDouble("lng"), fence.getDouble("radiusM")));
            } catch (JSONException e) {
                // A corrupt single entry must not take the rest of the list
                // down with it -- skip it, recover everything else.
            }
        }
        return out;
    }

    private static JSONObject readAll(SharedPreferences p) {
        String raw = p.getString(KEY_FENCES, null);
        if (raw == null) {
            return new JSONObject();
        }
        try {
            return new JSONObject(raw);
        } catch (JSONException e) {
            return new JSONObject();
        }
    }

    private static SharedPreferences prefs(Context context) {
        return context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    static final class Active {
        final String id;
        final double lat;
        final double lng;
        final double radiusM;

        Active(String id, double lat, double lng, double radiusM) {
            this.id = id;
            this.lat = lat;
            this.lng = lng;
            this.radiusM = radiusM;
        }
    }
}
