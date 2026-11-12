export function routeDataPaths(raw, type) {
  if (type === 'string') return raw?.trim() ? [raw.trim()] : [];
  try {
    const value = JSON.parse(raw || '[]');
    return Array.isArray(value) ? value.filter(path => typeof path === 'string' && path.trim()) : [];
  } catch { return []; }
}

// One table and one compatible destination only. Multiple files/roles are a
// user choice; this is draft autofill, never a command to start the workflow.
export function routeDataAutofillTarget(ports, candidates, values) {
  if (candidates.length !== 1 || candidates[0].portNames.length !== 1) return null;
  const candidate = candidates[0];
  const port = ports.find(port => port.name === candidate.portNames[0] && port.fileKind && ['array', 'string'].includes(port.type));
  if (!port || routeDataPaths(values[port.name], port.type).length) return null;
  return {port, candidate};
}

export function routeDataDraftWithFile(values, port, file) {
  if (!file?.relativePath) return values;
  const paths = routeDataPaths(values[port.name], port.type);
  if (port.type === 'string') return {...values, [port.name]: file.relativePath};
  if (paths.includes(file.relativePath) || paths.length >= (port.maxItems || 16)) return values;
  return {...values, [port.name]: JSON.stringify([...paths, file.relativePath])};
}

export function routeDataOmitEmpty(port, value) {
  return Boolean(port.fileKind && !port.required && (value === undefined || port.type === 'string' && typeof value === 'string' && !value.trim() || port.type === 'array' && Array.isArray(value) && value.length === 0));
}
export function routeDataInputReady(port, value) {
  if (value === undefined) return !port.required;
  if (routeDataOmitEmpty(port, value)) return true;
  const paths = port.type === 'string' && typeof value === 'string' ? [value] : port.type === 'array' && Array.isArray(value) ? value : [];
  return paths.length > 0 && paths.every(path => typeof path === 'string' && Boolean(path.trim()))
    && (!port.minItems || paths.length >= port.minItems) && (!port.maxItems || paths.length <= port.maxItems);
}
