use std::io;
pub mod pigpio;

#[cfg(target_os = "linux")]
pub use self::pigpio::*;

#[cfg(not(target_os = "linux"))]
#[allow(non_snake_case)]
mod pigpio_mock {

    pub const PI_DISABLE_FIFO_IF: u32 = 0;
    pub const PI_DISABLE_SOCK_IF: u32 = 0;
    pub const PI_OUTPUT: u32 = 0;
    pub const PI_WAVE_MODE_ONE_SHOT_SYNC: u32 = 0;

    pub unsafe fn gpioCfgInterfaces(_: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioInitialise() -> i32 {
        -1
    }

    pub unsafe fn gpioTick() -> u32 {
        0
    }
    pub unsafe fn gpioDelay(d: u32) -> u32 {
        d
    }

    pub unsafe fn gpioSerialReadOpen(_: u32, _: u32, _: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioSerialReadClose(_: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioSerialRead(_: u32, _: *mut std::ffi::c_void, _: usize) -> i32 {
        -1
    }

    pub unsafe fn gpioWaveAddNew() -> i32 {
        -1
    }
    pub unsafe fn gpioWaveAddSerial(
        _: u32,
        _: u32,
        _: u32,
        _: u32,
        _: u32,
        _: u32,
        _: *const ::std::os::raw::c_char,
    ) -> i32 {
        -1
    }
    pub unsafe fn gpioWaveCreate() -> i32 {
        -1
    }
    pub unsafe fn gpioWaveDelete(_: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioWaveTxBusy() -> i32 {
        -1
    }
    pub unsafe fn gpioWaveTxSend(_: u32, _: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioWaveTxStop() -> i32 {
        -1
    }

    pub unsafe fn gpioSetMode(_: u32, _: u32) -> i32 {
        -1
    }
    pub unsafe fn gpioWrite(_: u32, _: u32) -> i32 {
        -1
    }

    // pub  unsafe fn  gpio()->i32{-1};
    // pub  unsafe fn  gpio()->i32{-1};
    // pub  unsafe fn  gpio()->i32{-1};
}
#[cfg(not(target_os = "linux"))]
pub use self::pigpio_mock::*;

pub fn check(rc: i32) -> io::Result<u32> {
    if rc < 0 {
        Err(io::Error::new(
            io::ErrorKind::Other,
            format!("pigpio err={}", rc),
        ))
    } else {
        Ok(rc as u32)
    }
}

pub fn init() -> io::Result<()> {
    check(unsafe { gpioCfgInterfaces(PI_DISABLE_FIFO_IF | PI_DISABLE_SOCK_IF) })?;
    check(unsafe { gpioInitialise() })?;
    Ok(())
}

pub fn wave_tx_busy() -> io::Result<bool> {
    let rc = check(unsafe { gpioWaveTxBusy() })?;
    Ok(rc == 1)
}

pub fn wave_busy_wait(delay_step: u32, timeout: u32, err: &str) -> io::Result<u32> {
    let mut total = 0u32;
    loop {
        total += unsafe { gpioDelay(delay_step) };
        if !wave_tx_busy()? {
            break;
        }
        if total > timeout {
            return Err(io::Error::new(io::ErrorKind::TimedOut, err));
        }
    }
    Ok(total)
}

pub fn tick_since(start: u32) -> u32 {
    let end = unsafe { gpioTick() };
    end.wrapping_sub(start)
}

pub struct Wave(u32);
impl Wave {
    pub fn new_serial(
        tx: u32,
        baud: u32,
        data_bits: u32,
        stop_bits: u32,
        offset: u32,
        s: &[u8],
    ) -> io::Result<Wave> {
        check(unsafe { gpioWaveAddNew() })?;
        check(unsafe {
            gpioWaveAddSerial(
                tx,
                baud,
                data_bits,
                stop_bits,
                offset,
                s.len() as u32,
                s.as_ptr() as *mut ::std::os::raw::c_char,
            )
        })?;
        let wid = check(unsafe { gpioWaveCreate() })?;
        Ok(Wave(wid))
    }

    pub fn from_id(id: u32) -> Wave {
        Wave(id)
    }

    pub fn send(&self, mode: u32) -> io::Result<()> {
        check(unsafe { gpioWaveTxSend(self.0, mode) })?;
        Ok(())
    }
}

impl Drop for Wave {
    fn drop(&mut self) {
        let _ = unsafe { gpioWaveDelete(self.0) };
    }
}
