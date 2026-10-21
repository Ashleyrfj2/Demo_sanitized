const sensitivePattern = /(?:password|token|secret|authorization|cookie)/i;

export function maskText(value: string): string {
  const normalized = value.trim().slice(0, 120);
  return sensitivePattern.test(normalized) ? "[REDACTED]" : normalized;
}
