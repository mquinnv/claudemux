// Keeps the last few log lines for the phone page.
export function createLog(max = 10, onChange: (lines: string[]) => void = () => {}) {
  const lines: string[] = [];
  return {
    add(text: string, now: Date = new Date()): void {
      lines.unshift(`${now.toLocaleTimeString()} ${text}`);
      if (lines.length > max) lines.length = max;
      onChange([...lines]);
    },
    lines: (): string[] => [...lines],
  };
}
