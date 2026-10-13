import { HttpErrorResponse } from '@angular/common/http';
import { ratio } from './nodes';
import { parseGroups, teamErrorMessage } from './teams';

describe('admin helpers', () => {
  it('parses group lists separated by commas or new lines', () => {
    expect(parseGroups(' team-a, team-b\nteam-a\n\n')).toEqual(['team-a', 'team-b']);
    expect(parseGroups('')).toEqual([]);
  });

  it('maps API errors to Spanish messages', () => {
    expect(teamErrorMessage(new HttpErrorResponse({ status: 409 }))).toContain('Ya existe');
    expect(teamErrorMessage(new HttpErrorResponse({ status: 400 }))).toContain('no válidos');
    expect(teamErrorMessage(new Error('x'))).toContain('No se pudo guardar');
  });

  it('computes meter ratios safely', () => {
    expect(ratio(1, 4)).toBe(0.25);
    expect(ratio(8, 4)).toBe(1);
    expect(ratio(undefined, 4)).toBe(0);
    expect(ratio(1, 0)).toBe(0);
  });
});
