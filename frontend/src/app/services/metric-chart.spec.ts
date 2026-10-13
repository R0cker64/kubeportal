import { formatValue, toPolyline } from './metric-chart';

describe('metric chart helpers', () => {
  it('formats cores and bytes', () => {
    expect(formatValue(0.25, 'cores')).toBe('250m');
    expect(formatValue(1.5, 'cores')).toBe('1.50 cores');
    expect(formatValue(512, 'bytes')).toBe('512 B');
    expect(formatValue(64 * 1024 * 1024, 'bytes')).toBe('64 MiB');
  });

  it('scales points into the chart box', () => {
    expect(toPolyline([[0, 0], [10, 10]], 0, 10, 10)).toBe('4.0,156.0 596.0,4.0');
    expect(toPolyline([], 0, 10, 0)).toBe('');
  });
});
