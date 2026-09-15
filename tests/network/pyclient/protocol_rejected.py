import asyncio
import os
import sys

from pydantic_monty import AsyncMontyWebsocket


async def main() -> int:
    try:
        async with AsyncMontyWebsocket(os.environ['MONTY_URL'], request_timeout=30.0) as pool:
            async with pool.checkout() as session:
                await session.feed_run('1 + 1')
    except Exception as exc:  # noqa: BLE001
        print(f'REJECTED {exc}', flush=True)
        return 0
    print('UNEXPECTED success', flush=True)
    return 1


sys.exit(asyncio.run(main()))
