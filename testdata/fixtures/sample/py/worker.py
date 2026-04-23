"""Background worker for processing queued jobs."""

import asyncio
from typing import Optional


class JobQueue:
    """An in-memory FIFO job queue keyed by priority."""

    def __init__(self) -> None:
        self._jobs: list = []

    def enqueue(self, job: dict) -> None:
        """Add a job to the back of the queue."""
        self._jobs.append(job)

    async def dequeue(self) -> Optional[dict]:
        """Pop the oldest job, or return None if empty."""
        if not self._jobs:
            return None
        return self._jobs.pop(0)

    async def drain(self) -> int:
        """Drain the queue synchronously. Exercises self.method() resolution."""
        count = 0
        while True:
            job = await self.dequeue()  # -> worker.JobQueue.dequeue
            if job is None:
                return count
            count += 1


def start_worker(queue: JobQueue) -> None:
    """Start a worker loop that drains the given queue."""
    asyncio.run(_run(queue))


async def _run(queue: JobQueue) -> None:
    # Exercises resolution of parameter-typed method calls. Since we
    # don't do type inference, queue.drain stays as a textual ref.
    _ = await queue.drain()
    while True:
        job = await queue.dequeue()
        if job is None:
            await asyncio.sleep(0.01)
            continue
