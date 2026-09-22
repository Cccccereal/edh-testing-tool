package com.edhpowerlevel.client;

import android.annotation.SuppressLint;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import androidx.appcompat.app.AppCompatActivity;

import java.io.ByteArrayInputStream;

/**
 * The mobile shell: a full-screen WebView pointed at the in-process Go server.
 *
 * The gomobile-bound {@code mobile.Mobile} type exposes {@code start(long)} which
 * boots the analysis server on 127.0.0.1 and returns its base URL. This Activity calls
 * it once in {@link #onCreate}, then loads that URL and leaves everything else to the
 * existing web front-end.
 */
public class MainActivity extends AppCompatActivity {

    private WebView webView;

    @SuppressLint("SetJavaScriptEnabled")
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        String baseUrl = startServer();

        webView = new WebView(this);
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        // Default textZoom follows the system font scale; sizes in styles.css are
        // laid out for ~1.0-1.1x, so cap the boost (accessibility-friendly up to
        // 110%, prevents text overflowing its containers at 130%+).
        settings.setTextZoom(Math.min(settings.getTextZoom(), 110));
        // The served UI is trusted (our own front-end), but keep navigation sandboxed:
        // only loopback URLs load in the WebView; external links go to the system
        // browser (shouldOverrideUrlLoading below).
        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                String host = uri.getHost();
                if (host == null) return false;
                if (host.equals("127.0.0.1") || host.equals("localhost")) return false;
                // Real external link (update downloads, rule sites): return true to keep
                // it out of the WebView and hand it to the system browser. Without this
                // the navigation was silently dropped and external links were dead.
                try {
                    startActivity(new Intent(Intent.ACTION_VIEW, uri));
                } catch (ActivityNotFoundException e) {
                    // No browser app installed; drop the navigation instead of crashing.
                }
                return true;
            }

            @Override
            public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                String host = uri.getHost();
                // Loopback server and HTTPS (Scryfall images, EDHREC assets) are allowed;
                // only cleartext HTTP to third parties is blocked.
                if (host == null) return null;
                if (host.equals("127.0.0.1") || host.equals("localhost")) return null;
                if ("https".equalsIgnoreCase(uri.getScheme())) return null;
                return new WebResourceResponse("text/plain", "UTF-8",
                        new ByteArrayInputStream("".getBytes()));
            }
        });
        setContentView(webView);

        // gomobile bind can return "" on failure; fall back to a bare local placeholder.
        webView.loadUrl(baseUrl.isEmpty() ? "about:blank" : baseUrl);
    }

    /**
     * Calls the gomobile-generated start function. The Class/method names match the
     * gomobile bind output for a package {@code mobile} with a {@code Start} function
     * (class {@code Mobile}, static method {@code start}). gomobile's generated Java is
     * in the {@code mobile} package.
     */
    private String startServer() {
        try {
            return mobile.Mobile.start(0);
        } catch (Throwable t) {
            return "";
        }
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
            return;
        }
        super.onBackPressed();
    }
}
