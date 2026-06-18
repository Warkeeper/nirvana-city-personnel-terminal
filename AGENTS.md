# ncfms

- Resident codes are opaque strings: trim surrounding whitespace only; never strip leading zeroes, parse as numbers, or treat `01234` and `1234` as the same code.
- Excel/source workbook resident-code columns must be maintained as text so spreadsheet software does not strip leading zeroes.
