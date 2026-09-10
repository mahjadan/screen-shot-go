//go:build linux && cgo

// Portal parent-window export for xdg-desktop-portal on Wayland.
// Non-CGO Linux and other OSes use portal_parent_stub.go instead.

package main

/*
#cgo pkg-config: gtk4 gtk4-wayland
#include <gtk/gtk.h>
#include <gdk/gdk.h>
#include <stdlib.h>
#include <stdio.h>

#ifdef GDK_WINDOWING_WAYLAND
#include <gdk/wayland/gdkwayland.h>
#endif

#ifdef GDK_WINDOWING_X11
#include <gdk/x11/gdkx.h>
#endif

#ifdef GDK_WINDOWING_WAYLAND
typedef struct {
	char *handle;
	int timed_out;
} portal_export_ctx;

static void portal_on_exported(GdkToplevel *toplevel, const char *handle, gpointer user_data) {
	portal_export_ctx *ctx = user_data;
	ctx->handle = g_strdup(handle);
}

static gboolean portal_export_timeout(gpointer user_data) {
	portal_export_ctx *ctx = user_data;
	ctx->timed_out = 1;
	return G_SOURCE_REMOVE;
}

static char *portal_export_wayland_handle(GtkWindow *window) {
	GtkNative *native = GTK_NATIVE(window);
	GdkSurface *surface;
	portal_export_ctx ctx = {0};
	guint timeout_id;

	if (native == NULL) {
		return NULL;
	}

	surface = gtk_native_get_surface(native);
	if (surface == NULL || !GDK_IS_WAYLAND_SURFACE(surface)) {
		return NULL;
	}

	if (!gdk_wayland_toplevel_export_handle(GDK_TOPLEVEL(surface), portal_on_exported, &ctx, NULL)) {
		return NULL;
	}

	timeout_id = g_timeout_add_seconds(2, portal_export_timeout, &ctx);
	while (ctx.handle == NULL && !ctx.timed_out) {
		g_main_context_iteration(NULL, TRUE);
	}
	g_source_remove(timeout_id);

	if (ctx.handle == NULL) {
		return NULL;
	}

	char *formatted = g_strdup_printf("wayland:%s", ctx.handle);
	g_free(ctx.handle);
	return formatted;
}
#endif

#ifdef GDK_WINDOWING_X11
static char *portal_export_x11_handle(GtkWindow *window) {
	GtkNative *native = GTK_NATIVE(window);
	GdkSurface *surface;
	char *formatted;

	if (native == NULL) {
		return NULL;
	}

	surface = gtk_native_get_surface(native);
	if (surface == NULL || !GDK_IS_X11_SURFACE(surface)) {
		return NULL;
	}

	formatted = g_strdup_printf("x11:%lx", (unsigned long)gdk_x11_surface_get_xid(surface));
	return formatted;
}
#endif

static char *portal_export_parent_window(GtkWindow *window) {
#ifdef GDK_WINDOWING_WAYLAND
	char *wayland = portal_export_wayland_handle(window);
	if (wayland != NULL) {
		return wayland;
	}
#endif

#ifdef GDK_WINDOWING_X11
	char *x11 = portal_export_x11_handle(window);
	if (x11 != NULL) {
		return x11;
	}
#endif

	return NULL;
}

static GtkWindow *portal_create_anchor_window(void) {
	GtkWidget *window = gtk_window_new();
	gtk_window_set_default_size(GTK_WINDOW(window), 1, 1);
	gtk_window_set_decorated(GTK_WINDOW(window), FALSE);
	gtk_widget_set_opacity(window, 0.0);
	gtk_widget_realize(window);
	gtk_widget_set_visible(window, TRUE);

	for (int i = 0; i < 20; i++) {
		g_main_context_iteration(NULL, FALSE);
	}

	return GTK_WINDOW(window);
}

static void portal_destroy_anchor_window(GtkWindow *window) {
	if (window != NULL) {
		gtk_window_destroy(window);
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func acquirePortalParentWindow() (parentWindow string, cleanup func(), err error) {
	var anchor *C.GtkWindow

	application.InvokeSync(func() {
		anchor = C.portal_create_anchor_window()
		if anchor == nil {
			err = errors.New("failed to create portal anchor window")
			return
		}

		cHandle := C.portal_export_parent_window(anchor)
		if cHandle == nil {
			err = errors.New("failed to export portal parent window handle")
			return
		}
		defer C.free(unsafe.Pointer(cHandle))

		parentWindow = C.GoString(cHandle)
	})

	if anchor == nil {
		return "", nil, err
	}

	cleanup = func() {
		window := anchor
		application.InvokeSync(func() {
			C.portal_destroy_anchor_window(window)
		})
	}

	if err != nil {
		cleanup()
		return "", nil, err
	}

	return parentWindow, cleanup, nil
}
