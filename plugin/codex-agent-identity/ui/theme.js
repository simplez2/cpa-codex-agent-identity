(function () {
  'use strict';

  const storageKey = 'cli-proxy-theme';
  const messageType = 'cpa-codex-agent-identity:theme';
  const root = document.documentElement;
  const media = window.matchMedia('(prefers-color-scheme: dark)');
  const allowedVariables = new Set([
    '--bg-primary', '--bg-secondary', '--bg-tertiary', '--bg-hover', '--bg-quinary',
    '--floating-surface', '--floating-shadow', '--text-primary', '--text-secondary',
    '--text-tertiary', '--text-quaternary', '--text-muted', '--border-color',
    '--border-secondary', '--border-primary', '--border-hover', '--primary-color',
    '--primary-hover', '--primary-active', '--primary-contrast', '--success-color',
    '--quota-medium-color', '--warning-color', '--error-color', '--danger-color',
    '--info-color', '--warning-bg', '--warning-border', '--warning-text',
    '--success-badge-bg', '--success-badge-text', '--success-badge-border',
    '--failure-badge-bg', '--failure-badge-text', '--failure-badge-border',
    '--count-badge-bg', '--count-badge-text', '--shadow', '--shadow-lg',
    '--primary-8', '--primary-10', '--primary-30', '--amber-color', '--amber-text',
    '--amber-10', '--amber-30', '--destructive-color', '--destructive-10',
    '--destructive-30', '--muted-bg', '--muted-foreground', '--accent-bg',
    '--glass-bg', '--glass-bg-secondary', '--glass-border'
  ]);

  let parentControlled = false;
  let explicitTheme = false;
  let followSystem = true;

  function normalizeTheme(value) {
    return String(value || '').toLowerCase() === 'dark' ? 'dark' : 'white';
  }

  function storedTheme() {
    try {
      const raw = localStorage.getItem(storageKey);
      if (!raw) return '';
      const payload = JSON.parse(raw);
      const state = payload && payload.state ? payload.state : payload;
      if (!state || typeof state !== 'object') return '';
      followSystem = !state.theme || state.theme === 'auto' || state.theme === 'system';
      if (state.resolvedTheme) return normalizeTheme(state.resolvedTheme);
      if (state.theme && !followSystem) return normalizeTheme(state.theme);
    } catch (_) {
      return '';
    }
    return '';
  }

  function applyVariables(variables) {
    if (!variables || typeof variables !== 'object') return;
    allowedVariables.forEach(function (name) {
      root.style.removeProperty(name);
    });
    Object.keys(variables).forEach(function (name) {
      const value = variables[name];
      const normalized = typeof value === 'string' ? value.trim() : '';
      if (!allowedVariables.has(name) || !normalized || normalized.length > 256) return;
      root.style.setProperty(name, normalized);
    });
  }

  function applyTheme(theme, variables) {
    const normalized = normalizeTheme(theme);
    root.dataset.theme = normalized;
    root.style.colorScheme = normalized === 'dark' ? 'dark' : 'light';
    applyVariables(variables);
    root.dispatchEvent(new CustomEvent('cpa-theme-change', { detail: { theme: normalized } }));
  }

  const params = new URLSearchParams(window.location.search);
  if (params.get('embed') === 'cpamc') root.dataset.embed = 'cpamc';
  const queryTheme = params.get('theme');
  explicitTheme = queryTheme === 'dark' || queryTheme === 'white' || queryTheme === 'light';
  applyTheme(explicitTheme ? queryTheme : (storedTheme() || (media.matches ? 'dark' : 'white')));

  window.addEventListener('message', function (event) {
    if (window.parent === window || event.source !== window.parent) return;
    const data = event.data || {};
    if (data.type !== messageType) return;
    parentControlled = true;
    applyTheme(data.theme, data.variables);
  });

  window.addEventListener('storage', function (event) {
    if (event.key !== storageKey || parentControlled || explicitTheme) return;
    applyTheme(storedTheme() || (media.matches ? 'dark' : 'white'));
  });

  function handleMediaChange() {
    if (!parentControlled && !explicitTheme && followSystem) {
      applyTheme(media.matches ? 'dark' : 'white');
    }
  }
  if (typeof media.addEventListener === 'function') media.addEventListener('change', handleMediaChange);
  else if (typeof media.addListener === 'function') media.addListener(handleMediaChange);
})();
