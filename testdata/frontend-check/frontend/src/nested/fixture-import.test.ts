import { expect, it } from 'vitest'
import documented from '../../../docs/imported.json'
import teaching from '../../../testdata/imported.json'

it('executes with literal repository docs and testdata JSON imports', () => {
  expect(documented.kind).toBe('documentation fixture')
  expect(teaching.kind).toBe('teaching fixture')
})
