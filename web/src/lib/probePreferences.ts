export interface ProbePrototypePreferences {
  slowFirstOutputThresholdMs: number;
}

interface StoredProbePrototypePreferences extends ProbePrototypePreferences {
  version: 2;
}

const STORAGE_KEY = 'jieshan.prototype.probe-preferences.v2';
const MIN_SLOW_FIRST_OUTPUT_THRESHOLD_MS = 1_000;
const MAX_SLOW_FIRST_OUTPUT_THRESHOLD_MS = 120_000;

export const DEFAULT_PROBE_PROTOTYPE_PREFERENCES: ProbePrototypePreferences = {
  slowFirstOutputThresholdMs: 15_000,
};

function normalizeSlowFirstOutputThreshold(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return DEFAULT_PROBE_PROTOTYPE_PREFERENCES.slowFirstOutputThresholdMs;
  return Math.min(
    MAX_SLOW_FIRST_OUTPUT_THRESHOLD_MS,
    Math.max(MIN_SLOW_FIRST_OUTPUT_THRESHOLD_MS, Math.round(numeric)),
  );
}

export function loadProbePrototypePreferences(): ProbePrototypePreferences {
  if (typeof window === 'undefined') return DEFAULT_PROBE_PROTOTYPE_PREFERENCES;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_PROBE_PROTOTYPE_PREFERENCES;
    const stored = JSON.parse(raw) as Partial<StoredProbePrototypePreferences>;
    if (stored.version !== 2) return DEFAULT_PROBE_PROTOTYPE_PREFERENCES;
    return {
      slowFirstOutputThresholdMs: normalizeSlowFirstOutputThreshold(stored.slowFirstOutputThresholdMs),
    };
  } catch {
    return DEFAULT_PROBE_PROTOTYPE_PREFERENCES;
  }
}

export function saveProbePrototypePreferences(
  preferences: ProbePrototypePreferences,
): ProbePrototypePreferences {
  const normalized = {
    slowFirstOutputThresholdMs: normalizeSlowFirstOutputThreshold(preferences.slowFirstOutputThresholdMs),
  };
  if (typeof window === 'undefined') return normalized;
  try {
    const stored: StoredProbePrototypePreferences = { version: 2, ...normalized };
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // Keep the prototype usable when browser storage is unavailable.
  }
  return normalized;
}
