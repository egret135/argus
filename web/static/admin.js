(function() {
    "use strict";

    var PAGE_LIMIT = 50;
    var STATS_INTERVAL = 5000;
    var AUTO_REFRESH_INTERVAL = 2000;

    // State
    var state = {
        autoRefresh: false,
        autoRefreshTimer: null,
        highestSeq: 0,
        cursorStack: [],  // stack of before_seq values for "previous" pages
        currentBeforeSeq: null,
        hasMore: false,
        knownSources: {},
        dashboardOpen: false
    };

    // DOM refs
    var tbody = document.getElementById("log-tbody");
    var emptyState = document.getElementById("empty-state");
    var btnPrev = document.getElementById("btn-prev");
    var btnNext = document.getElementById("btn-next");
    var btnSearch = document.getElementById("btn-search");
    var btnAutoRefresh = document.getElementById("btn-auto-refresh");
    var autoRefreshDot = document.getElementById("auto-refresh-dot");
    var pageInfo = document.getElementById("page-info");
    var filterQuery = document.getElementById("filter-query");
    var filterLevel = document.getElementById("filter-level");
    var filterSource = document.getElementById("filter-source");
    var filterTime = document.getElementById("filter-time");
    var btnDashboard = document.getElementById("btn-dashboard");
    var statsDashboard = document.getElementById("stats-dashboard");

    // --- Utility ---
    function formatNumber(n) {
        if (n === null || n === undefined) return "—";
        return Number(n).toLocaleString();
    }

    function truncate(str, max) {
        if (!str) return "";
        return str.length > max ? str.substring(0, max) + "…" : str;
    }

    function formatBytes(bytes) {
        if (bytes === 0) return "0 B";
        var units = ["B", "KB", "MB", "GB"];
        var i = 0;
        var b = bytes;
        while (b >= 1024 && i < units.length - 1) {
            b /= 1024;
            i++;
        }
        return (i === 0 ? b : b.toFixed(1)) + " " + units[i];
    }

    function escapeForDisplay(str) {
        // Using textContent handles escaping; this is a fallback for attribute contexts.
        var d = document.createElement("div");
        d.textContent = str;
        return d.innerHTML;
    }

    // --- Stats ---
    function refreshStats() {
        fetch("/api/stats", { credentials: "same-origin" })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                document.getElementById("stat-ingest").textContent = formatNumber(data.ingest_total);
                document.getElementById("stat-dropped").textContent = formatNumber(data.ingest_dropped);
                document.getElementById("stat-errors").textContent = formatNumber(data.parse_error);

                var bufCount = (data.ringbuffer_head || 0) - (data.ringbuffer_tail || 0);
                if (bufCount < 0) bufCount = 0;
                document.getElementById("stat-buffer").textContent = formatNumber(bufCount);

                // Populate sources from index if available via log data (handled in loadLogs)

                // Dashboard KPIs
                if (state.dashboardOpen) {
                    var capacity = data.ringbuffer_capacity || 50000;
                    var used = (data.ringbuffer_head || 0);
                    var tail = (data.ringbuffer_tail || 0);
                    var bufUsed = tail - used;
                    if (bufUsed < 0) bufUsed = 0;
                    var pct = capacity > 0 ? Math.round(bufUsed / capacity * 100) : 0;

                    // Ring chart
                    var circumference = 326.73;
                    var offset = circumference * (1 - pct / 100);
                    var ringFg = document.getElementById("ring-fg");
                    if (ringFg) {
                        ringFg.style.strokeDashoffset = offset;
                        ringFg.style.stroke = pct > 90 ? "#DC2626" : pct > 70 ? "#F59E0B" : "#4F6BF6";
                    }
                    var ringLabel = document.getElementById("ring-label");
                    if (ringLabel) ringLabel.textContent = pct + "%";
                    var bufUsedEl = document.getElementById("buf-used");
                    if (bufUsedEl) bufUsedEl.textContent = formatNumber(bufUsed);
                    var bufCapEl = document.getElementById("buf-cap");
                    if (bufCapEl) bufCapEl.textContent = formatNumber(capacity);

                    // Ingestion card
                    var dashIngest = document.getElementById("dash-ingest");
                    if (dashIngest) dashIngest.textContent = formatNumber(data.ingest_total);
                    var dashDropped = document.getElementById("dash-dropped");
                    if (dashDropped) dashDropped.textContent = formatNumber(data.ingest_dropped);
                    var dashParseErr = document.getElementById("dash-parse-err");
                    if (dashParseErr) dashParseErr.textContent = formatNumber(data.parse_error);

                    // WAL card
                    var dashWalSize = document.getElementById("dash-wal-size");
                    if (dashWalSize) dashWalSize.textContent = formatBytes(data.wal_bytes || 0);
                    var dashWalCompact = document.getElementById("dash-wal-compact");
                    if (dashWalCompact) dashWalCompact.textContent = formatNumber(data.wal_compaction_total);
                    var dashWalTrunc = document.getElementById("dash-wal-trunc");
                    if (dashWalTrunc) dashWalTrunc.textContent = formatNumber(data.wal_truncated);

                    // Alert card
                    var dashAlertOk = document.getElementById("dash-alert-ok");
                    if (dashAlertOk) dashAlertOk.textContent = formatNumber(data.alert_send_ok);
                    var dashAlertFail = document.getElementById("dash-alert-fail");
                    if (dashAlertFail) dashAlertFail.textContent = formatNumber(data.alert_send_failed);
                    var dashAlertQfull = document.getElementById("dash-alert-qfull");
                    if (dashAlertQfull) dashAlertQfull.textContent = formatNumber(data.alert_queue_full);
                    var dashAlertDedup = document.getElementById("dash-alert-dedup");
                    if (dashAlertDedup) dashAlertDedup.textContent = formatNumber(data.alert_deduplicated);

                    // System health row
                    var dashIdxRebuilds = document.getElementById("dash-idx-rebuilds");
                    if (dashIdxRebuilds) dashIdxRebuilds.textContent = formatNumber(data.index_rebuild_total);
                    var dashIdxDur = document.getElementById("dash-idx-dur");
                    if (dashIdxDur) dashIdxDur.textContent = (data.index_rebuild_duration_ms || 0) + "ms";
                    var dashTailerRecon = document.getElementById("dash-tailer-recon");
                    if (dashTailerRecon) dashTailerRecon.textContent = formatNumber(data.tailer_reconnects);
                    var dashDockerErr = document.getElementById("dash-docker-err");
                    if (dashDockerErr) dashDockerErr.textContent = formatNumber(data.docker_parse_error);
                    var dashAlertThrottle = document.getElementById("dash-alert-throttle");
                    if (dashAlertThrottle) dashAlertThrottle.textContent = formatNumber(data.alert_throttled);
                    var dashAlertTimeout = document.getElementById("dash-alert-timeout");
                    if (dashAlertTimeout) dashAlertTimeout.textContent = formatNumber(data.alert_inflight_timeout);

                    refreshDistribution();
                }
            })
            .catch(function() {});
    }

    // --- Build Query Params ---
    function buildParams(overrides) {
        var params = new URLSearchParams();
        params.set("limit", PAGE_LIMIT);

        var q = filterQuery.value.trim();
        if (q) params.set("q", q);

        var level = filterLevel.value;
        if (level) params.set("level", level);

        var source = filterSource.value;
        if (source) params.set("source", source);

        var timeRange = filterTime.value;
        if (timeRange) params.set("time_range", timeRange);

        if (overrides) {
            Object.keys(overrides).forEach(function(k) {
                if (overrides[k] !== null && overrides[k] !== undefined) {
                    params.set(k, overrides[k]);
                }
            });
        }

        return params.toString();
    }

    // --- Render Logs ---
    function createLogRow(log) {
        var tr = document.createElement("tr");
        var levelLower = (log.level || "").toLowerCase();
        if (levelLower === "error") tr.className = "row-error";
        if (levelLower === "fatal") tr.className = "row-fatal";
        tr.dataset.seqId = log.seq_id;

        var tdTime = document.createElement("td");
        tdTime.textContent = log.timestamp || "";
        tr.appendChild(tdTime);

        var tdLevel = document.createElement("td");
        var badge = document.createElement("span");
        badge.className = "level-badge level-" + (log.level || "");
        badge.textContent = log.level || "";
        tdLevel.appendChild(badge);
        tr.appendChild(tdLevel);

        var tdService = document.createElement("td");
        tdService.textContent = log.service || "";
        tdService.title = log.service || "";
        tr.appendChild(tdService);

        var tdSource = document.createElement("td");
        tdSource.textContent = log.source || "";
        tdSource.title = log.source || "";
        tr.appendChild(tdSource);

        var tdMsg = document.createElement("td");
        tdMsg.textContent = truncate(log.message || "", 200);
        tdMsg.title = log.message || "";
        tr.appendChild(tdMsg);

        // Track sources
        if (log.source && !state.knownSources[log.source]) {
            state.knownSources[log.source] = true;
            var opt = document.createElement("option");
            opt.value = log.source;
            opt.textContent = log.source;
            filterSource.appendChild(opt);
        }

        // Click to expand detail
        tr.addEventListener("click", function() {
            toggleDetail(tr, log);
        });

        return tr;
    }

    function createDetailRow(log) {
        var tr = document.createElement("tr");
        tr.className = "detail-row";
        var td = document.createElement("td");
        td.colSpan = 5;

        var parts = [];
        parts.push(labelValue("Seq ID", String(log.seq_id)));
        parts.push(labelValue("Timestamp", log.timestamp || ""));
        parts.push(labelValue("Level", log.level || ""));
        parts.push(labelValue("Service", log.service || ""));
        parts.push(labelValue("Source", log.source || ""));
        parts.push(labelValue("Message", log.message || ""));

        if (log.trace_id) parts.push(labelValue("Trace ID", log.trace_id));
        if (log.caller) parts.push(labelValue("Caller", log.caller));
        if (log.stack_trace) parts.push(labelValue("Stack Trace", log.stack_trace));
        if (log.extra && Object.keys(log.extra).length > 0) {
            parts.push(labelValue("Extra", JSON.stringify(log.extra, null, 2)));
        }

        // Build detail content safely using textContent per element
        parts.forEach(function(pair, idx) {
            var labelSpan = document.createElement("span");
            labelSpan.className = "detail-label";
            labelSpan.textContent = pair[0] + ": ";
            td.appendChild(labelSpan);

            var valSpan = document.createElement("span");
            valSpan.textContent = pair[1];
            td.appendChild(valSpan);

            if (idx < parts.length - 1) {
                td.appendChild(document.createTextNode("\n"));
            }
        });

        tr.appendChild(td);
        return tr;
    }

    function labelValue(label, value) {
        return [label, value];
    }

    function toggleDetail(tr, log) {
        var next = tr.nextElementSibling;
        if (next && next.classList.contains("detail-row")) {
            next.remove();
        } else {
            var detail = createDetailRow(log);
            tr.parentNode.insertBefore(detail, tr.nextSibling);
        }
    }

    function renderLogs(logs, append) {
        if (!append) {
            tbody.innerHTML = "";
        }

        if (!append && logs.length === 0) {
            emptyState.classList.remove("hidden");
        } else {
            emptyState.classList.add("hidden");
        }

        logs.forEach(function(log) {
            var row = createLogRow(log);
            if (append) {
                // Prepend new logs to top
                tbody.insertBefore(row, tbody.firstChild);
            } else {
                tbody.appendChild(row);
            }
        });
    }

    // --- Load Logs ---
    function loadLogs(overrides) {
        var qs = buildParams(overrides);
        fetch("/api/logs?" + qs, { credentials: "same-origin" })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var logs = data.logs || [];
                state.hasMore = data.has_more;

                renderLogs(logs, false);

                // Track highest seq for auto-refresh
                if (logs.length > 0) {
                    state.highestSeq = Math.max(state.highestSeq, logs[0].seq_id);
                }

                updatePagination(logs);
            })
            .catch(function() {});
    }

    // --- Pagination ---
    function updatePagination(logs) {
        btnPrev.disabled = state.cursorStack.length === 0;
        btnNext.disabled = !state.hasMore;
        pageInfo.textContent = "Page " + (state.cursorStack.length + 1);
    }

    function nextPage() {
        // Get the lowest seq_id on current page as cursor
        var rows = tbody.querySelectorAll("tr:not(.detail-row)");
        if (rows.length === 0) return;
        var lastRow = rows[rows.length - 1];
        var lastSeq = parseInt(lastRow.dataset.seqId, 10);

        // Push current position to stack for "back"
        state.cursorStack.push(state.currentBeforeSeq);
        state.currentBeforeSeq = lastSeq;

        loadLogs({ before_seq: lastSeq });
    }

    function prevPage() {
        if (state.cursorStack.length === 0) return;
        var prevCursor = state.cursorStack.pop();
        state.currentBeforeSeq = prevCursor;

        if (prevCursor !== null && prevCursor !== undefined) {
            loadLogs({ before_seq: prevCursor });
        } else {
            loadLogs({});
        }
    }

    // --- Auto-Refresh ---
    function toggleAutoRefresh() {
        state.autoRefresh = !state.autoRefresh;
        if (state.autoRefresh) {
            btnAutoRefresh.innerHTML = "";
            autoRefreshDot = document.createElement("span");
            autoRefreshDot.className = "auto-refresh-indicator on";
            autoRefreshDot.id = "auto-refresh-dot";
            btnAutoRefresh.appendChild(autoRefreshDot);
            btnAutoRefresh.appendChild(document.createTextNode(" Auto-refresh: ON"));
            btnAutoRefresh.classList.add("btn-active");

            state.autoRefreshTimer = setInterval(pollNewLogs, AUTO_REFRESH_INTERVAL);
            localStorage.setItem("argus_auto_refresh", "1");
        } else {
            btnAutoRefresh.innerHTML = "";
            autoRefreshDot = document.createElement("span");
            autoRefreshDot.className = "auto-refresh-indicator off";
            autoRefreshDot.id = "auto-refresh-dot";
            btnAutoRefresh.appendChild(autoRefreshDot);
            btnAutoRefresh.appendChild(document.createTextNode(" Auto-refresh: OFF"));
            btnAutoRefresh.classList.remove("btn-active");

            if (state.autoRefreshTimer) {
                clearInterval(state.autoRefreshTimer);
                state.autoRefreshTimer = null;
            }
            localStorage.setItem("argus_auto_refresh", "0");
        }
    }

    function pollNewLogs() {
        if (!state.autoRefresh) return;
        if (state.highestSeq === 0) return;

        var params = new URLSearchParams();
        params.set("after_seq", state.highestSeq);
        params.set("limit", PAGE_LIMIT);

        fetch("/api/logs?" + params.toString(), { credentials: "same-origin" })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var logs = data.logs || [];
                if (logs.length === 0) return;

                // Logs come in descending order; reverse to prepend oldest-first
                logs.reverse();

                // Update highest seq
                state.highestSeq = Math.max(state.highestSeq, logs[logs.length - 1].seq_id);

                emptyState.classList.add("hidden");

                logs.forEach(function(log) {
                    var row = createLogRow(log);
                    tbody.insertBefore(row, tbody.firstChild);
                });
            })
            .catch(function() {});
    }

    // --- Distribution Charts ---
    function refreshDistribution() {
        fetch("/api/stats/distribution", { credentials: "same-origin" })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                renderDistChart("chart-levels", data.levels || {}, "level");
                renderDistChart("chart-sources", data.sources || {}, "source");
            })
            .catch(function() {});
    }

    function renderDistChart(containerId, counts, type) {
        var container = document.getElementById(containerId);
        if (!container) return;

        var entries = [];
        Object.keys(counts).forEach(function(key) {
            entries.push({ label: key, count: counts[key] });
        });
        entries.sort(function(a, b) { return b.count - a.count; });

        if (type === "source") {
            entries = entries.slice(0, 10);
        }

        if (entries.length === 0) {
            container.innerHTML = '<div style="color:#94A3B8;font-size:13px;padding:8px 0;">No data</div>';
            return;
        }

        var maxCount = entries[0].count;
        var levelOrder = ["DEBUG", "INFO", "WARN", "ERROR", "FATAL"];
        if (type === "level") {
            entries.sort(function(a, b) {
                return levelOrder.indexOf(a.label) - levelOrder.indexOf(b.label);
            });
        }

        var html = "";
        entries.forEach(function(e) {
            var pct = maxCount > 0 ? (e.count / maxCount * 100) : 0;
            var cls = type === "level" ? "dist-bar-" + e.label : "dist-bar-source";
            var displayLabel = e.label;
            if (type === "source") {
                var parts = e.label.split("/");
                displayLabel = parts[parts.length - 1] || e.label;
            }
            html += '<div class="dist-bar-row ' + cls + '" data-type="' + type + '" data-value="' + escapeForDisplay(e.label) + '">';
            html += '<span class="dist-bar-label" title="' + escapeForDisplay(e.label) + '">' + escapeForDisplay(displayLabel) + '</span>';
            html += '<div class="dist-bar-track"><div class="dist-bar-fill" style="width:' + pct + '%"></div></div>';
            html += '<span class="dist-bar-count">' + formatNumber(e.count) + '</span>';
            html += '</div>';
        });
        container.innerHTML = html;

        var rows = container.querySelectorAll(".dist-bar-row");
        for (var i = 0; i < rows.length; i++) {
            (function(row) {
                row.addEventListener("click", function() {
                    var t = row.getAttribute("data-type");
                    var v = row.getAttribute("data-value");
                    if (t === "level") {
                        filterLevel.value = v;
                    } else if (t === "source") {
                        filterSource.value = v;
                    }
                    doSearch();
                });
            })(rows[i]);
        }
    }

    // --- Dashboard Toggle ---
    function toggleDashboard() {
        state.dashboardOpen = !state.dashboardOpen;
        if (state.dashboardOpen) {
            statsDashboard.classList.remove("hidden");
            btnDashboard.classList.add("active");
            localStorage.setItem("argus_dashboard", "1");
            refreshStats();
        } else {
            statsDashboard.classList.add("hidden");
            btnDashboard.classList.remove("active");
            localStorage.setItem("argus_dashboard", "0");
        }
    }

    // --- Search ---
    function doSearch() {
        state.cursorStack = [];
        state.currentBeforeSeq = null;
        loadLogs({});
    }

    // --- Init ---
    function init() {
        btnSearch.addEventListener("click", doSearch);
        filterQuery.addEventListener("keydown", function(e) {
            if (e.key === "Enter") doSearch();
        });
        btnNext.addEventListener("click", nextPage);
        btnPrev.addEventListener("click", prevPage);
        btnAutoRefresh.addEventListener("click", toggleAutoRefresh);

        btnDashboard.addEventListener("click", toggleDashboard);

        // Restore dashboard state from localStorage
        if (localStorage.getItem("argus_dashboard") === "1") {
            toggleDashboard();
        }
        if (localStorage.getItem("argus_auto_refresh") === "1") {
            toggleAutoRefresh();
        }

        // Initial load
        loadLogs({});
        refreshStats();

        // Stats refresh every 5 seconds
        setInterval(refreshStats, STATS_INTERVAL);
    }

    init();
})();
