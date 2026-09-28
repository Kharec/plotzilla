//go:build linux

package main

/*
#cgo pkg-config: gtk+-3.0 gio-2.0
#include <gtk/gtk.h>

// ---------------------------------------------------------------- colour scheme

// GTK 3 does not follow the desktop's light/dark preference on its own (only
// libadwaita apps do). Apply it to the native parts of the window — file
// choosers and message dialogs — and keep following it when it changes.
//
// The preference comes from the XDG settings portal, which GNOME, KDE Plasma
// and other desktops implement; GNOME's own setting is the fallback when no
// portal answers. Without an explicit preference, GTK keeps what the desktop
// configured for it (settings.ini, XSettings).

enum { SCHEME_DEFAULT, SCHEME_DARK, SCHEME_LIGHT };

static gboolean configured_dark;
static gboolean have_configured_dark;

static void apply_scheme(int scheme) {
	GtkSettings *settings = gtk_settings_get_default();
	if (settings == NULL) return;
	if (!have_configured_dark) {
		g_object_get(settings, "gtk-application-prefer-dark-theme", &configured_dark, NULL);
		have_configured_dark = TRUE;
	}
	gboolean dark = scheme == SCHEME_DARK ? TRUE : scheme == SCHEME_LIGHT ? FALSE : configured_dark;
	g_object_set(settings, "gtk-application-prefer-dark-theme", dark, NULL);
}

// GNOME fallback: org.gnome.desktop.interface color-scheme.

static void apply_gnome_scheme(GSettings *settings) {
	gchar *scheme = g_settings_get_string(settings, "color-scheme");
	apply_scheme(g_strcmp0(scheme, "prefer-dark") == 0    ? SCHEME_DARK
	             : g_strcmp0(scheme, "prefer-light") == 0 ? SCHEME_LIGHT
	                                                      : SCHEME_DEFAULT);
	g_free(scheme);
}

static void on_gnome_scheme_changed(GSettings *settings, gchar *key, gpointer data) {
	apply_gnome_scheme(settings);
}

static void follow_gnome_scheme(void) {
	GSettingsSchemaSource *source = g_settings_schema_source_get_default();
	if (source == NULL) return;
	GSettingsSchema *schema = g_settings_schema_source_lookup(source, "org.gnome.desktop.interface", TRUE);
	if (schema == NULL) return;
	gboolean has_key = g_settings_schema_has_key(schema, "color-scheme");
	g_settings_schema_unref(schema);
	if (!has_key) return;

	// Never freed: it must outlive the window to keep delivering changes.
	GSettings *settings = g_settings_new("org.gnome.desktop.interface");
	apply_gnome_scheme(settings);
	g_signal_connect(settings, "changed::color-scheme", G_CALLBACK(on_gnome_scheme_changed), NULL);
}

// XDG portal: org.freedesktop.appearance color-scheme, 0 = no preference,
// 1 = prefer dark, 2 = prefer light.

#define PORTAL_NAME "org.freedesktop.portal.Desktop"
#define PORTAL_PATH "/org/freedesktop/portal/desktop"
#define PORTAL_SETTINGS "org.freedesktop.portal.Settings"
#define APPEARANCE "org.freedesktop.appearance"

static int scheme_from_portal(GVariant *value) {
	// Settings.Read wraps the value in one variant too many; unwrap them all.
	g_variant_ref(value);
	while (g_variant_is_of_type(value, G_VARIANT_TYPE_VARIANT)) {
		GVariant *inner = g_variant_get_variant(value);
		g_variant_unref(value);
		value = inner;
	}
	guint32 scheme = g_variant_is_of_type(value, G_VARIANT_TYPE_UINT32) ? g_variant_get_uint32(value) : 0;
	g_variant_unref(value);
	return scheme == 1 ? SCHEME_DARK : scheme == 2 ? SCHEME_LIGHT : SCHEME_DEFAULT;
}

static void on_portal_setting_changed(GDBusConnection *bus, const gchar *sender, const gchar *path,
                                      const gchar *iface, const gchar *signal, GVariant *params, gpointer data) {
	const gchar *ns, *key;
	GVariant *value;
	g_variant_get(params, "(&s&sv)", &ns, &key, &value);
	if (g_strcmp0(ns, APPEARANCE) == 0 && g_strcmp0(key, "color-scheme") == 0) apply_scheme(scheme_from_portal(value));
	g_variant_unref(value);
}

static void on_portal_read(GObject *source, GAsyncResult *res, gpointer data) {
	GDBusConnection *bus = G_DBUS_CONNECTION(source);
	GVariant *reply = g_dbus_connection_call_finish(bus, res, NULL);
	if (reply == NULL) {
		// No portal, or one without the setting.
		g_object_unref(bus);
		follow_gnome_scheme();
		return;
	}
	GVariant *value;
	g_variant_get(reply, "(v)", &value);
	apply_scheme(scheme_from_portal(value));
	g_variant_unref(value);
	g_variant_unref(reply);
	// The connection and the subscription live as long as the process.
	g_dbus_connection_signal_subscribe(bus, PORTAL_NAME, PORTAL_SETTINGS, "SettingChanged", PORTAL_PATH, APPEARANCE,
	                                   G_DBUS_SIGNAL_FLAGS_NONE, on_portal_setting_changed, NULL, NULL);
}

static void on_session_bus(GObject *source, GAsyncResult *res, gpointer data) {
	GDBusConnection *bus = g_bus_get_finish(res, NULL);
	if (bus == NULL) {
		follow_gnome_scheme();
		return;
	}
	g_dbus_connection_call(bus, PORTAL_NAME, PORTAL_PATH, PORTAL_SETTINGS, "Read",
	                       g_variant_new("(ss)", APPEARANCE, "color-scheme"), G_VARIANT_TYPE("(v)"),
	                       G_DBUS_CALL_FLAGS_NONE, 5000, NULL, on_portal_read, NULL);
}

static gboolean follow_color_scheme(gpointer data) {
	g_bus_get(G_BUS_TYPE_SESSION, NULL, on_session_bus, NULL);
	return G_SOURCE_REMOVE;
}

// Safe to call from any thread: the work runs on the GTK main loop.
static void schedule_follow_color_scheme(void) {
	g_idle_add(follow_color_scheme, NULL);
}

// ---------------------------------------------------------------- window frame

// What the in-app header bar needs to look like the desktop's other windows:
// GTK's decoration layout, which every desktop feeds to GTK in its own way
// (GNOME and Plasma settings, XSettings on Xfce, MATE, Cinnamon, settings.ini
// elsewhere), and whether the window really is translucent. Without a
// compositor the window has no alpha channel and rounded corners would show
// black wedges.
//
// GTK may only be touched on its main loop, while the caller is a Wails
// goroutine: the request is answered from an idle callback. It is reference
// counted so that a caller giving up on a stalled loop cannot free it under
// the callback.

typedef struct {
	GMutex lock;
	GCond answered;
	gboolean done;
	gchar *layout;
	gboolean translucent;
	gint refs;
} FrameRequest;

static void frame_request_unref(FrameRequest *req) {
	if (!g_atomic_int_dec_and_test(&req->refs)) return;
	g_mutex_clear(&req->lock);
	g_cond_clear(&req->answered);
	g_free(req->layout);
	g_free(req);
}

static gboolean window_is_translucent(void) {
	GdkScreen *screen = gdk_screen_get_default();
	if (screen == NULL || !gdk_screen_is_composited(screen)) return FALSE;
	GdkVisual *rgba = gdk_screen_get_rgba_visual(screen);
	gboolean translucent = FALSE;
	GList *windows = gtk_window_list_toplevels();
	for (GList *w = windows; w != NULL; w = w->next) {
		if (rgba != NULL && gtk_widget_get_visual(GTK_WIDGET(w->data)) == rgba) translucent = TRUE;
	}
	g_list_free(windows);
	return translucent;
}

static gboolean answer_frame_request(gpointer data) {
	FrameRequest *req = data;
	gchar *layout = NULL;
	GtkSettings *settings = gtk_settings_get_default();
	if (settings != NULL) g_object_get(settings, "gtk-decoration-layout", &layout, NULL);
	gboolean translucent = window_is_translucent();

	g_mutex_lock(&req->lock);
	req->layout = layout;
	req->translucent = translucent;
	req->done = TRUE;
	g_cond_signal(&req->answered);
	g_mutex_unlock(&req->lock);
	frame_request_unref(req);
	return G_SOURCE_REMOVE;
}

// Returns FALSE when the main loop did not answer in time. *layout is
// g_malloc'ed (possibly NULL) and must be freed with g_free.
static gboolean query_window_frame(gchar **layout, gboolean *translucent) {
	FrameRequest *req = g_new0(FrameRequest, 1);
	g_mutex_init(&req->lock);
	g_cond_init(&req->answered);
	req->refs = 2;
	g_idle_add(answer_frame_request, req);

	gint64 deadline = g_get_monotonic_time() + 2 * G_TIME_SPAN_SECOND;
	g_mutex_lock(&req->lock);
	while (!req->done) {
		if (!g_cond_wait_until(&req->answered, &req->lock, deadline)) break;
	}
	gboolean done = req->done;
	*layout = done ? g_strdup(req->layout) : NULL;
	*translucent = done && req->translucent;
	g_mutex_unlock(&req->lock);
	frame_request_unref(req);
	return done;
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

func followSystemColorScheme() {
	C.schedule_follow_color_scheme()
}

// defaultButtonLayout is used when GTK cannot be asked: GNOME's default.
const defaultButtonLayout = "appmenu:close"

// systemWindowFrame reads GTK's title bar button layout and whether the
// window is composited. It must not be called from the GTK main thread.
func systemWindowFrame() WindowFrame {
	var layout *C.gchar
	var translucent C.gboolean
	if C.query_window_frame(&layout, &translucent) == C.FALSE {
		return WindowFrame{Buttons: defaultButtonLayout, Rounded: true}
	}
	defer C.g_free(C.gpointer(unsafe.Pointer(layout)))
	buttons := defaultButtonLayout
	if layout != nil {
		if s := strings.TrimSpace(C.GoString((*C.char)(unsafe.Pointer(layout)))); s != "" {
			buttons = s
		}
	}
	return WindowFrame{Buttons: buttons, Rounded: translucent != C.FALSE}
}
