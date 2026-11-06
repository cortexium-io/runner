export default {
  test: {
    include: ['src/**/*.test.ts'],
    exclude: ['src/omitted.test.ts'],
    fileParallelism: false,
    maxWorkers: 1,
  },
}
